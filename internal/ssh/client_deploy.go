package ssh

import (
	"fmt"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
)

const ratholeVersion = "v0.5.0"

const systemdServiceTemplate = `[Unit]
Description=Rathole Client
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rathole --client /etc/rathole/client.toml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// DeployClient installs rathole on a machine and configures it with the given tunnels.
func DeployClient(machine *db.Machine, vpsHost string, tunnels []db.Tunnel, ratholeClientConfig string) (string, error) {
	c, err := NewClient(machine.Host, machine.Port, machine.User, machine.SSHKey)
	if err != nil {
		return "", fmt.Errorf("connect to machine %s: %w", machine.Name, err)
	}
	defer c.Close()

	var logs strings.Builder

	// Detect architecture
	logs.WriteString("=== Detecting architecture ===\n")
	archOut, err := c.Run("uname -m")
	if err != nil {
		return logs.String(), fmt.Errorf("detect arch: %w", err)
	}
	arch := strings.TrimSpace(archOut)
	logs.WriteString(arch + "\n")

	goArch := "x86_64"
	if strings.Contains(arch, "aarch64") || strings.Contains(arch, "arm64") {
		goArch = "aarch64"
	}

	steps := []struct {
		desc string
		cmd  string
	}{
		{"Install curl", "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl 2>/dev/null || yum install -y curl 2>/dev/null || true"},
		{"Download rathole", fmt.Sprintf(
			`curl -fsSL -o /tmp/rathole.tar.gz "https://github.com/rapiz1/rathole/releases/download/%s/rathole-%s-unknown-linux-gnu.tar.gz" && tar -xzf /tmp/rathole.tar.gz -C /tmp && mv /tmp/rathole /usr/local/bin/rathole && chmod +x /usr/local/bin/rathole`,
			ratholeVersion, goArch,
		)},
		{"Create config dir", "mkdir -p /etc/rathole"},
	}

	for _, step := range steps {
		logs.WriteString(fmt.Sprintf("=== %s ===\n", step.desc))
		out, err := c.Run(step.cmd)
		logs.WriteString(out)
		if err != nil {
			logs.WriteString(fmt.Sprintf("WARN: %v\n", err))
		}
	}

	// Write client config
	logs.WriteString("=== Writing rathole client config ===\n")
	if err := c.WriteFile("/etc/rathole/client.toml", ratholeClientConfig); err != nil {
		return logs.String(), fmt.Errorf("write client config: %w", err)
	}

	// Write systemd service
	logs.WriteString("=== Writing systemd service ===\n")
	if err := c.WriteFile("/etc/systemd/system/rathole-client.service", systemdServiceTemplate); err != nil {
		return logs.String(), fmt.Errorf("write systemd service: %w", err)
	}

	// Enable and start service
	logs.WriteString("=== Enabling rathole-client service ===\n")
	out, err := c.Run("systemctl daemon-reload && systemctl enable rathole-client && systemctl restart rathole-client")
	logs.WriteString(out)
	if err != nil {
		logs.WriteString(fmt.Sprintf("WARN: %v\n", err))
	}

	_ = vpsHost
	return logs.String(), nil
}
