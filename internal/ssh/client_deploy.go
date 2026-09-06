package ssh

import (
	_ "embed"
	"fmt"
	"io"
	"strings"

	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/paths"
)

//go:embed templates/client-install.sh
var clientInstallScript string

//go:embed templates/rathole-client.service
var clientServiceTemplate string

func buildClientServiceUnit(username, ratholebin string) string {
	unit := clientServiceTemplate
	unit = strings.ReplaceAll(unit, "{{.Username}}", username)
	unit = strings.ReplaceAll(unit, "{{.RatholeBin}}", ratholebin)
	unit = strings.ReplaceAll(unit, "{{.ConfigPath}}", paths.RatholeClientConfig)
	return unit
}

func DeployClient(client *SSHClient, machineID, username, config string, logWriter io.Writer) error {
	fmt.Fprintln(logWriter, "=== Deploying Rathole Client ===")

	// Resolve sudo prefix once: empty string when already root, "sudo" otherwise.
	sudoPrefix, _ := client.Execute(`[ "$(id -u)" -eq 0 ] && echo "" || echo "sudo"`)
	sudoPrefix = strings.TrimSpace(sudoPrefix)
	sudo := func(cmd string) string {
		if sudoPrefix == "" {
			return cmd
		}
		return sudoPrefix + " " + cmd
	}

	// Step 0: Stop any existing service.
	fmt.Fprintln(logWriter, "Step 0: Stopping any existing rathole-client service...")
	_, _ = client.Execute(sudo("systemctl stop rathole-client") + " 2>/dev/null || true")

	fmt.Fprintln(logWriter, "Step 1: Installing rathole binary...")
	if err := client.UploadFile([]byte(build.InjectVersions(clientInstallScript)), "/tmp/client-install.sh"); err != nil {
		return fmt.Errorf("failed to upload install script: %w", err)
	}
	if err := ExecuteWithOutput(client, "chmod +x /tmp/client-install.sh && /tmp/client-install.sh", logWriter); err != nil {
		return fmt.Errorf("failed to install rathole client: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 2: Writing rathole client config...")

	// Write the client config under the consolidated /etc/gopher layout. A full
	// redeploy adopts the new paths wholesale (it rewrites the unit below), which
	// also migrates a legacy machine forward. The legacy /etc/rathole copy, if
	// any, is left untouched — orphaned but harmless — since the unit now points
	// at the new path.
	if _, err := client.Execute(sudo("mkdir -p " + paths.RatholeDir)); err != nil {
		return fmt.Errorf("failed to create %s: %w", paths.RatholeDir, err)
	}

	// Determine the rathole binary path.
	ratholebin, err := client.Execute("which rathole || echo /usr/local/bin/rathole")
	if err != nil {
		ratholebin = "/usr/local/bin/rathole"
	}
	ratholebin = strings.TrimSpace(ratholebin)

	if err := client.UploadFile([]byte(config), paths.RatholeClientConfig); err != nil {
		return fmt.Errorf("failed to write client config: %w", err)
	}

	// Fix ownership so the agent/SSH user can update the config later.
	if _, err := client.Execute(sudo(fmt.Sprintf("chown %s %s %s", username, paths.RatholeDir, paths.RatholeClientConfig))); err != nil {
		return fmt.Errorf("failed to fix ownership: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 3: Installing system service...")
	serviceUnit := buildClientServiceUnit(username, ratholebin)

	// Write to temp file first, then move into place.
	tmpService := "/tmp/.gopher-rathole-client.service"
	if err := client.UploadFile([]byte(serviceUnit), tmpService); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}
	if _, err := client.Execute(sudo(fmt.Sprintf("mv %s /etc/systemd/system/rathole-client.service", tmpService))); err != nil {
		return fmt.Errorf("failed to install service file: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 4: Enabling and starting service...")
	cmds := []string{
		sudo("systemctl daemon-reload"),
		sudo("systemctl enable rathole-client"),
		sudo("systemctl restart rathole-client"),
	}
	if err := ExecuteCommands(client, cmds, logWriter); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	fmt.Fprintln(logWriter, "=== Client Deployment Complete ===")
	return nil
}
