package ssh

import (
	"fmt"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
)

const dockerComposeContent = `version: "3.8"
services:
  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - caddy_data:/data
      - caddy_config:/config
    networks:
      - tunnel_net

  rathole:
    image: rapiz1/rathole:latest
    restart: unless-stopped
    command: --server /config/server.toml
    ports:
      - "2333:2333"
    volumes:
      - ./rathole-server.toml:/config/server.toml
    networks:
      - tunnel_net

volumes:
  caddy_data:
  caddy_config:

networks:
  tunnel_net:
    driver: bridge
`

// SetupVPS installs Docker + Docker Compose on the VPS and deploys the stack.
func SetupVPS(vps *db.VPSConfig, caddyfile, ratholeConfig string) (string, error) {
	c, err := NewClient(vps.Host, vps.Port, vps.User, vps.SSHKey)
	if err != nil {
		return "", fmt.Errorf("connect to VPS: %w", err)
	}
	defer c.Close()

	var logs strings.Builder

	steps := []struct {
		desc string
		cmd  string
	}{
		{"Update apt", "DEBIAN_FRONTEND=noninteractive apt-get update -qq"},
		{"Install deps", "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl ca-certificates gnupg lsb-release"},
		{"Install Docker", `curl -fsSL https://get.docker.com | sh`},
		{"Start Docker", "systemctl enable docker && systemctl start docker"},
		{"Create app dir", "mkdir -p /opt/gopher"},
	}

	for _, step := range steps {
		logs.WriteString(fmt.Sprintf("=== %s ===\n", step.desc))
		out, err := c.Run(step.cmd)
		logs.WriteString(out)
		if err != nil {
			logs.WriteString(fmt.Sprintf("ERROR: %v\n", err))
			return logs.String(), fmt.Errorf("step %q failed: %w", step.desc, err)
		}
	}

	// Write config files
	files := map[string]string{
		"/opt/gopher/docker-compose.yml":  dockerComposeContent,
		"/opt/gopher/Caddyfile":           caddyfile,
		"/opt/gopher/rathole-server.toml": ratholeConfig,
	}
	for path, content := range files {
		logs.WriteString(fmt.Sprintf("=== Writing %s ===\n", path))
		if err := c.WriteFile(path, content); err != nil {
			logs.WriteString(fmt.Sprintf("ERROR: %v\n", err))
			return logs.String(), fmt.Errorf("write %s: %w", path, err)
		}
	}

	// Start the stack
	logs.WriteString("=== Starting Docker Compose stack ===\n")
	out, err := c.Run("cd /opt/gopher && docker compose up -d --pull always 2>&1 || docker-compose up -d --pull always 2>&1")
	logs.WriteString(out)
	if err != nil {
		return logs.String(), fmt.Errorf("docker compose up: %w", err)
	}

	return logs.String(), nil
}

// DeployVPSConfig updates only the config files and reloads services.
func DeployVPSConfig(vps *db.VPSConfig, caddyfile, ratholeConfig string) (string, error) {
	c, err := NewClient(vps.Host, vps.Port, vps.User, vps.SSHKey)
	if err != nil {
		return "", fmt.Errorf("connect to VPS: %w", err)
	}
	defer c.Close()

	var logs strings.Builder

	files := map[string]string{
		"/opt/gopher/Caddyfile":           caddyfile,
		"/opt/gopher/rathole-server.toml": ratholeConfig,
	}
	for path, content := range files {
		logs.WriteString(fmt.Sprintf("=== Writing %s ===\n", path))
		if err := c.WriteFile(path, content); err != nil {
			logs.WriteString(fmt.Sprintf("ERROR: %v\n", err))
			return logs.String(), fmt.Errorf("write %s: %w", path, err)
		}
	}

	logs.WriteString("=== Reloading services ===\n")
	out, err := c.Run("cd /opt/gopher && docker compose restart 2>&1 || docker-compose restart 2>&1")
	logs.WriteString(out)
	if err != nil {
		return logs.String(), fmt.Errorf("docker compose restart: %w", err)
	}
	return logs.String(), nil
}
