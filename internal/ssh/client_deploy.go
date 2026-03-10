package ssh

import (
	_ "embed"
	"fmt"
	"io"
)

//go:embed templates/client-install.sh
var clientInstallScript string

func buildClientServiceUnit(username string) string {
	return fmt.Sprintf(`[Unit]
Description=Rathole Tunnel Client
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
ExecStart=/usr/local/bin/rathole /etc/rathole/client.toml
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, username)
}

func DeployClient(client *SSHClient, machineID, username, config string, logWriter io.Writer) error {
	fmt.Fprintln(logWriter, "=== Deploying Rathole Client ===")

	fmt.Fprintln(logWriter, "Step 1: Installing rathole...")
	if err := client.UploadFile([]byte(clientInstallScript), "/tmp/client-install.sh"); err != nil {
		return fmt.Errorf("failed to upload install script: %w", err)
	}
	if err := ExecuteWithOutput(client, "chmod +x /tmp/client-install.sh && /tmp/client-install.sh", logWriter); err != nil {
		return fmt.Errorf("failed to install rathole client: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 2: Writing rathole client config...")
	// /etc/rathole/ is owned by the SSH user after bootstrap; UploadFileSudo handles
	// legacy machines where the file ended up root-owned.
	if err := client.UploadFileSudo([]byte(config), "/etc/rathole/client.toml", username); err != nil {
		return fmt.Errorf("failed to write client config: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 3: Installing systemd service...")
	serviceUnit := buildClientServiceUnit(username)
	if err := client.UploadFileSudo([]byte(serviceUnit), "/etc/systemd/system/rathole-client.service", ""); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
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
