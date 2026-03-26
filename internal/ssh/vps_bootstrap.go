package ssh

import (
	_ "embed"
	"fmt"
	"io"
)

//go:embed templates/caddy-install.sh
var caddyInstallScript string

//go:embed templates/rathole-install.sh
var ratholeInstallScript string

//go:embed templates/Caddyfile.initial
var initialCaddyfileTemplate string

//go:embed templates/rathole-server.toml.initial
var initialRatholeConfigTemplate string

//go:embed templates/caddy.service
var caddyServiceTemplate string

//go:embed templates/rathole-server-vps.service
var ratholeServerServiceTemplate string

func BootstrapVPS(client *SSHClient, logWriter io.Writer) error {
	fmt.Fprintln(logWriter, "=== Starting VPS Bootstrap ===")

	fmt.Fprintln(logWriter, "Step 1: Installing Caddy...")
	if err := client.UploadFile([]byte(caddyInstallScript), "/tmp/caddy-install.sh"); err != nil {
		return fmt.Errorf("failed to upload caddy install script: %w", err)
	}
	if err := ExecuteWithOutput(client, "chmod +x /tmp/caddy-install.sh && /tmp/caddy-install.sh", logWriter); err != nil {
		return fmt.Errorf("failed to install caddy: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 2: Installing Rathole...")
	if err := client.UploadFile([]byte(ratholeInstallScript), "/tmp/rathole-install.sh"); err != nil {
		return fmt.Errorf("failed to upload rathole install script: %w", err)
	}
	if err := ExecuteWithOutput(client, "chmod +x /tmp/rathole-install.sh && /tmp/rathole-install.sh", logWriter); err != nil {
		return fmt.Errorf("failed to install rathole: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 3: Creating /opt/gopher directory...")
	if err := ExecuteWithOutput(client, "sudo mkdir -p /opt/gopher", logWriter); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 4: Uploading initial Caddyfile...")
	if err := client.UploadFile([]byte(initialCaddyfileTemplate), "/tmp/Caddyfile"); err != nil {
		return fmt.Errorf("failed to upload Caddyfile: %w", err)
	}
	if err := ExecuteWithOutput(client, "sudo mv /tmp/Caddyfile /opt/gopher/Caddyfile && sudo chown caddy:caddy /opt/gopher/Caddyfile", logWriter); err != nil {
		return fmt.Errorf("failed to move Caddyfile: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 5: Uploading initial rathole config...")
	if err := client.UploadFile([]byte(initialRatholeConfigTemplate), "/tmp/rathole-server.toml"); err != nil {
		return fmt.Errorf("failed to upload rathole config: %w", err)
	}
	if err := ExecuteWithOutput(client, "sudo mv /tmp/rathole-server.toml /opt/gopher/rathole-server.toml && sudo chmod 644 /opt/gopher/rathole-server.toml", logWriter); err != nil {
		return fmt.Errorf("failed to move rathole config: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 6: Installing systemd services...")
	if err := client.UploadFile([]byte(caddyServiceTemplate), "/tmp/caddy.service"); err != nil {
		return fmt.Errorf("failed to upload caddy service: %w", err)
	}
	if err := ExecuteWithOutput(client, "sudo mv /tmp/caddy.service /etc/systemd/system/caddy.service", logWriter); err != nil {
		return fmt.Errorf("failed to move caddy service: %w", err)
	}

	if err := client.UploadFile([]byte(ratholeServerServiceTemplate), "/tmp/rathole-server.service"); err != nil {
		return fmt.Errorf("failed to upload rathole service: %w", err)
	}
	if err := ExecuteWithOutput(client, "sudo mv /tmp/rathole-server.service /etc/systemd/system/rathole-server.service", logWriter); err != nil {
		return fmt.Errorf("failed to move rathole service: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 7: Starting services...")
	if err := ExecuteWithOutput(client, "sudo systemctl daemon-reload && sudo systemctl enable caddy && sudo systemctl enable rathole-server", logWriter); err != nil {
		return fmt.Errorf("failed to enable services: %w", err)
	}

	if err := ExecuteWithOutput(client, "sudo systemctl start caddy && sudo systemctl start rathole-server", logWriter); err != nil {
		return fmt.Errorf("failed to start services: %w", err)
	}

	fmt.Fprintln(logWriter, "=== VPS Bootstrap Complete ===")
	return nil
}
