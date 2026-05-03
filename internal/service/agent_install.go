package service

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
	sshpkg "github.com/smalex-z/gopher/internal/ssh"
)

// AgentInstaller pushes the gopher-agent to an existing machine via the SSH
// back-channel and brings the rathole config in sync so the VPS can reach it.
//
// All steps are idempotent: a partial previous run is safe to retry.
type AgentInstaller struct {
	local *LocalSetupService
}

func NewAgentInstaller(local *LocalSetupService) *AgentInstaller {
	return &AgentInstaller{local: local}
}

// Install runs the full installation flow against an existing machine. On
// success the machine record is marked AgentInstalled=true and AgentInstallError
// is cleared. On failure the error message is persisted so the migration UI
// can surface it for manual retry.
func (i *AgentInstaller) Install(machineID string) error {
	machine, err := db.GetMachine(machineID)
	if err != nil {
		return err
	}

	if err := i.installInner(machine); err != nil {
		machine.AgentInstallError = truncate(err.Error(), 500)
		machine.UpdatedAt = time.Now()
		_ = db.UpdateMachine(machine)
		return err
	}

	now := time.Now()
	machine.AgentInstalled = true
	machine.AgentInstallError = ""
	machine.AgentLastSeen = &now
	machine.UpdatedAt = now
	if err := db.UpdateMachine(machine); err != nil {
		return fmt.Errorf("save machine: %w", err)
	}
	return nil
}

func (i *AgentInstaller) installInner(machine *db.Machine) error {
	if machine.TunnelPort == 0 {
		return fmt.Errorf("machine has no tunnel port — cannot SSH for install")
	}

	// Allocate agent fields if missing (they're populated for newly-bootstrapped
	// machines but old records pre-date the schema).
	dirty := false
	if machine.AgentLocalPort == 0 {
		machine.AgentLocalPort = agentLocalPortDefault
		dirty = true
	}
	if machine.AgentRemotePort == 0 {
		port, err := db.NextAgentPort()
		if err != nil {
			return fmt.Errorf("allocate agent remote port: %w", err)
		}
		machine.AgentRemotePort = port
		dirty = true
	}
	if machine.AgentToken == "" {
		machine.AgentToken = shortToken()
		dirty = true
	}
	if machine.AgentRatholeToken == "" {
		machine.AgentRatholeToken = shortToken()
		dirty = true
	}
	if dirty {
		if err := db.UpdateMachine(machine); err != nil {
			return fmt.Errorf("persist agent allocation: %w", err)
		}
	}

	// Connect to the machine through its existing SSH back-tunnel.
	sshKey, err := db.GetSSHKeyForMachine(machine)
	if err != nil {
		return fmt.Errorf("ssh key lookup: %w", err)
	}
	client, err := sshpkg.NewClient(TunnelDialHost(machine), machine.TunnelPort, machine.Username, sshKey.PrivateKey)
	if err != nil {
		return fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	// Detect arch — uname -m is reliable across the distros we already support.
	archOut, err := client.Execute("uname -m")
	if err != nil {
		return fmt.Errorf("detect arch: %w", err)
	}
	arch := strings.TrimSpace(archOut)
	var binaryName string
	switch arch {
	case "x86_64":
		binaryName = "gopher-agent-linux-amd64"
	case "aarch64", "arm64":
		binaryName = "gopher-agent-linux-arm64"
	default:
		return fmt.Errorf("unsupported arch %q (only amd64/arm64 supported today)", arch)
	}

	// Reach back to the VPS for the binary. The client uses the same VPS
	// address rathole already knows about — pulled out of the rathole config.
	settings, err := db.GetSettings()
	if err != nil {
		return fmt.Errorf("settings lookup: %w", err)
	}
	vpsHost := settings.ServerHost
	if vpsHost == "" {
		vpsHost = settings.Domain
	}
	if vpsHost == "" {
		return fmt.Errorf("no VPS host configured; set domain or server_host first")
	}
	vpsURL := vpsHost
	if !strings.HasPrefix(vpsURL, "http://") && !strings.HasPrefix(vpsURL, "https://") {
		vpsURL = "https://" + vpsURL
		// Caddy may not be configured (LocalSetupDone=false). Fall back to
		// HTTP-on-dashboard-port in that case.
		if !settings.LocalSetupDone {
			vpsURL = fmt.Sprintf("http://%s:%d", vpsHost, dashboardPort)
		}
	}

	// Stage the install script. Single shell invocation = single SSH session,
	// so we get atomic-ish behaviour even if the connection drops mid-run.
	script := buildAgentInstallScript(agentInstallParams{
		BinaryURL: vpsURL + "/static/agents/" + binaryName,
		Token:     machine.AgentToken,
		Port:      machine.AgentLocalPort,
		Username:  machine.Username,
	})

	var stdout bytes.Buffer
	if err := client.ExecuteWithOutput(script, &stdout); err != nil {
		return fmt.Errorf("install script failed: %w (%s)", err, truncate(stdout.String(), 400))
	}

	// Push an updated rathole client config that includes the agent service,
	// then restart rathole on the client so the new tunnel comes up.
	clientCfg := config.GenerateMachineSSHClientConfig(vpsHost, machine)
	if err := i.pushRatholeConfig(client, clientCfg); err != nil {
		return fmt.Errorf("update rathole client config: %w", err)
	}

	// Reconcile the VPS-side rathole config so the new server.services entry
	// for this machine's agent is bound.
	if err := i.local.ReconcileServerConfig(); err != nil {
		log.Printf("agent install: VPS rathole reconcile failed: %v", err)
		// Not fatal — the next reconcile loop will pick it up.
	}

	// Verify the agent is reachable through the new back-channel. Wait up to
	// 30 seconds for rathole to re-establish the tunnel after the restart.
	if err := i.verifyAgentReachable(machine); err != nil {
		return fmt.Errorf("agent installed but not reachable yet: %w", err)
	}
	return nil
}

func (i *AgentInstaller) pushRatholeConfig(client *sshpkg.SSHClient, cfg string) error {
	if err := client.UploadFileSudo([]byte(cfg), "/etc/rathole/client.toml", ""); err != nil {
		return err
	}
	// Restart rathole-client to pick up the new service.
	if _, err := client.Execute("sudo -n systemctl restart rathole-client"); err != nil {
		return fmt.Errorf("restart rathole-client: %w", err)
	}
	return nil
}

func (i *AgentInstaller) verifyAgentReachable(machine *db.Machine) error {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		client := NewAgentClient(machine)
		v, err := client.Version(ctx)
		cancel()
		if err == nil {
			machine.AgentVersion = v
			return nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("timed out waiting for agent")
}

type agentInstallParams struct {
	BinaryURL string
	Token     string
	Port      int
	Username  string
}

// buildAgentInstallScript returns a single shell script that downloads the
// agent, writes the systemd unit + config, and starts the service. Idempotent:
// running it twice is fine (configs get overwritten, systemctl restart is
// safe). Uses heredocs to keep the inputs hermetic — no quoting headaches.
func buildAgentInstallScript(p agentInstallParams) string {
	return fmt.Sprintf(`set -e
SUDO=$(command -v sudo >/dev/null 2>&1 && echo "sudo -n" || echo "")
TMP=/tmp/gopher-agent.$$
trap 'rm -f "$TMP"' EXIT

if command -v curl >/dev/null 2>&1; then
  curl -fsSL --insecure %q -o "$TMP"
elif command -v wget >/dev/null 2>&1; then
  wget -q --no-check-certificate %q -O "$TMP"
else
  echo "no curl or wget available" >&2
  exit 1
fi

$SUDO install -m 0755 "$TMP" /usr/local/bin/gopher-agent
$SUDO mkdir -p /etc/gopher-agent
$SUDO tee /etc/gopher-agent/config.env >/dev/null <<'GOPHER_AGENT_CONFIG_EOF'
GOPHER_AGENT_TOKEN=%s
GOPHER_AGENT_PORT=%d
GOPHER_AGENT_UNIT=rathole-client.service
GOPHER_AGENT_CONFIG_EOF
$SUDO chmod 640 /etc/gopher-agent/config.env
$SUDO chown root:%s /etc/gopher-agent/config.env || true

$SUDO tee /etc/systemd/system/gopher-agent.service >/dev/null <<'GOPHER_AGENT_UNIT_EOF'
[Unit]
Description=Gopher Agent (control-plane back-channel)
After=network.target

[Service]
Type=simple
User=%s
EnvironmentFile=/etc/gopher-agent/config.env
ExecStart=/usr/local/bin/gopher-agent
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
GOPHER_AGENT_UNIT_EOF

$SUDO systemctl daemon-reload
$SUDO systemctl enable gopher-agent >/dev/null 2>&1 || true
$SUDO systemctl restart gopher-agent
echo "gopher-agent installed and running"
`,
		p.BinaryURL, p.BinaryURL, p.Token, p.Port, p.Username, p.Username)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
