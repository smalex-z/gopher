package ssh

import (
"fmt"
"io"
)

func DeployVPS(client *SSHClient, caddyfile, ratholeConfig string, logWriter io.Writer) error {
fmt.Fprintln(logWriter, "=== Deploying VPS Configuration ===")

fmt.Fprintln(logWriter, "Uploading Caddyfile...")
if err := client.UploadFile([]byte(caddyfile), "/opt/gopher/Caddyfile"); err != nil {
return fmt.Errorf("failed to upload Caddyfile: %w", err)
}

fmt.Fprintln(logWriter, "Uploading rathole server config...")
if err := client.UploadFile([]byte(ratholeConfig), "/opt/gopher/rathole-server.toml"); err != nil {
return fmt.Errorf("failed to upload rathole config: %w", err)
}

fmt.Fprintln(logWriter, "Restarting Caddy...")
if err := ExecuteWithOutput(client, "cd /opt/gopher && docker compose restart caddy", logWriter); err != nil {
return fmt.Errorf("failed to restart caddy: %w", err)
}

fmt.Fprintln(logWriter, "Restarting rathole server...")
if err := ExecuteWithOutput(client, "cd /opt/gopher && docker compose restart rathole", logWriter); err != nil {
return fmt.Errorf("failed to restart rathole: %w", err)
}

fmt.Fprintln(logWriter, "=== VPS Deployment Complete ===")
return nil
}
