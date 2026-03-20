package ssh

import (
	_ "embed"
	"fmt"
	"io"
)

//go:embed templates/docker-install.sh
var dockerInstallScript string

const dockerComposeContent = `version: '3.8'
services:
  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - caddy_data:/data
      - caddy_config:/config
    networks:
      - tunnel_net

  rathole:
    image: rapiz1/rathole:latest
    restart: unless-stopped
    ports:
      - "2333:2333"
    volumes:
      - ./rathole-server.toml:/app/config.toml
    command: ["--server", "/app/config.toml"]
    networks:
      - tunnel_net

volumes:
  caddy_data:
  caddy_config:

networks:
  tunnel_net:
    driver: bridge
`

const initialCaddyfile = `{
    email admin@example.com
}

# ===== BEGIN CUSTOM CONFIGURATION =====
# Everything below this line will NOT be overwritten on deploy.
# Add any custom Caddy directives or site blocks here.
# ===== END CUSTOM CONFIGURATION =====
`

const initialRatholeConfig = `[server]
bind_addr = "0.0.0.0:2333"

[server.default_token]
default_token = "changeme"

# ===== BEGIN CUSTOM CONFIGURATION =====
# Everything below this line will NOT be overwritten on deploy.
# Add any custom rathole service entries here.
# ===== END CUSTOM CONFIGURATION =====
`

func BootstrapVPS(client *SSHClient, logWriter io.Writer) error {
	fmt.Fprintln(logWriter, "=== Starting VPS Bootstrap ===")

	fmt.Fprintln(logWriter, "Step 1: Installing Docker...")
	if err := client.UploadFile([]byte(dockerInstallScript), "/tmp/docker-install.sh"); err != nil {
		return fmt.Errorf("failed to upload docker install script: %w", err)
	}
	if err := ExecuteWithOutput(client, "chmod +x /tmp/docker-install.sh && /tmp/docker-install.sh", logWriter); err != nil {
		return fmt.Errorf("failed to install docker: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 2: Creating /opt/gopher directory...")
	if err := ExecuteWithOutput(client, "mkdir -p /opt/gopher", logWriter); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 3: Uploading docker-compose.yml...")
	if err := client.UploadFile([]byte(dockerComposeContent), "/opt/gopher/docker-compose.yml"); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 4: Uploading initial Caddyfile...")
	if err := client.UploadFile([]byte(initialCaddyfile), "/opt/gopher/Caddyfile"); err != nil {
		return fmt.Errorf("failed to upload Caddyfile: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 5: Uploading initial rathole config...")
	if err := client.UploadFile([]byte(initialRatholeConfig), "/opt/gopher/rathole-server.toml"); err != nil {
		return fmt.Errorf("failed to upload rathole config: %w", err)
	}

	fmt.Fprintln(logWriter, "Step 6: Starting services...")
	if err := ExecuteWithOutput(client, "cd /opt/gopher && docker compose up -d", logWriter); err != nil {
		return fmt.Errorf("failed to start services: %w", err)
	}

	fmt.Fprintln(logWriter, "=== VPS Bootstrap Complete ===")
	return nil
}
