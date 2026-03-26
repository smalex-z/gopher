package ssh

import (
	_ "embed"
	"fmt"
	"io"
	"strings"
)

//go:embed templates/client-install.sh
var clientInstallScript string

//go:embed templates/rathole-client.service
var clientServiceTemplate string

func buildClientServiceUnit(username, ratholebin string) string {
	unit := clientServiceTemplate
	unit = strings.ReplaceAll(unit, "{{.Username}}", username)
	unit = strings.ReplaceAll(unit, "{{.RatholeBin}}", ratholebin)
	return unit
}

func DeployClient(client *SSHClient, machineID, username, config string, logWriter io.Writer) error {
	fmt.Fprintln(logWriter, "=== Deploying Rathole Client ===")

	// Step 0: Stop any existing service.
	fmt.Fprintln(logWriter, "Step 0: Stopping any existing rathole-client service...")
	_, _ = client.Execute("sudo systemctl stop rathole-client 2>/dev/null || true")

	fmt.Fprintln(logWriter, "Step 1: Installing rathole binary...")
	if err := client.UploadFile([]byte(clientInstallScript), "/tmp/client-install.sh"); err != nil {
		return fmt.Errorf("failed to upload install script: %w", err)
	}
	if err := ExecuteWithOutput(client, "chmod +x /tmp/client-install.sh && /tmp/client-install.sh", logWriter); err != nil {
		return fmt.Errorf("failed to install rathole client: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 2: Writing rathole client config...")

	// Create /etc/rathole directory and write config with proper ownership.
	if _, err := client.Execute("sudo mkdir -p /etc/rathole"); err != nil {
		return fmt.Errorf("failed to create /etc/rathole: %w", err)
	}

	// Determine the rathole binary path.
	ratholebin, err := client.Execute("which rathole || echo /usr/local/bin/rathole")
	if err != nil {
		ratholebin = "/usr/local/bin/rathole"
	}
	ratholebin = strings.TrimSpace(ratholebin)

	if err := client.UploadFile([]byte(config), "/etc/rathole/client.toml"); err != nil {
		return fmt.Errorf("failed to write client config: %w", err)
	}

	// Fix ownership so the SSH user can update the config later.
	if _, err := client.Execute(fmt.Sprintf("sudo chown %s /etc/rathole /etc/rathole/client.toml", username)); err != nil {
		return fmt.Errorf("failed to fix ownership: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 3: Installing system service...")
	serviceUnit := buildClientServiceUnit(username, ratholebin)

	// Write to temp file first, then use sudo to move into place.
	tmpService := "/tmp/.gopher-rathole-client.service"
	if err := client.UploadFile([]byte(serviceUnit), tmpService); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}
	if _, err := client.Execute(fmt.Sprintf("sudo mv %s /etc/systemd/system/rathole-client.service", tmpService)); err != nil {
		return fmt.Errorf("failed to install service file: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 4: Enabling and starting service...")
	cmds := []string{
		"sudo systemctl daemon-reload",
		"sudo systemctl enable rathole-client",
		"sudo systemctl restart rathole-client",
	}
	if err := ExecuteCommands(client, cmds, logWriter); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	fmt.Fprintln(logWriter, "=== Client Deployment Complete ===")
	return nil
}
