package ssh

import (
	"fmt"
	"io"
	"strings"
)

const customBeginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="

// preserveCustomSection replaces the custom section in newContent with the one
// found in oldContent, so user edits between the markers are never overwritten.
func preserveCustomSection(newContent, oldContent string) string {
	beginIdx := strings.Index(oldContent, customBeginMarker)
	if beginIdx == -1 {
		return newContent
	}
	// Keep everything from the begin marker to the end of the old file.
	preserved := strings.TrimRight(oldContent[beginIdx:], "\n")

	newBeginIdx := strings.Index(newContent, customBeginMarker)
	if newBeginIdx == -1 {
		return newContent
	}
	return newContent[:newBeginIdx] + preserved + "\n"
}

func DeployVPS(client *SSHClient, caddyfile, ratholeConfig string, logWriter io.Writer) error {
	fmt.Fprintln(logWriter, "=== Deploying VPS Configuration ===")

	fmt.Fprintln(logWriter, "Uploading Caddyfile...")
	if existing, err := client.ReadFile("/opt/gopher/Caddyfile"); err == nil {
		caddyfile = preserveCustomSection(caddyfile, string(existing))
	}
	if err := client.UploadFile([]byte(caddyfile), "/tmp/Caddyfile"); err != nil {
		return fmt.Errorf("failed to upload Caddyfile: %w", err)
	}
	if err := ExecuteWithOutput(client, "sudo mv /tmp/Caddyfile /opt/gopher/Caddyfile && sudo chown caddy:caddy /opt/gopher/Caddyfile", logWriter); err != nil {
		return fmt.Errorf("failed to move Caddyfile: %w", err)
	}

	fmt.Fprintln(logWriter, "Uploading rathole server config...")
	if existing, err := client.ReadFile("/opt/gopher/rathole-server.toml"); err == nil {
		ratholeConfig = preserveCustomSection(ratholeConfig, string(existing))
	}
	if err := client.UploadFile([]byte(ratholeConfig), "/tmp/rathole-server.toml"); err != nil {
		return fmt.Errorf("failed to upload rathole config: %w", err)
	}
	if err := ExecuteWithOutput(client, "sudo mv /tmp/rathole-server.toml /opt/gopher/rathole-server.toml", logWriter); err != nil {
		return fmt.Errorf("failed to move rathole config: %w", err)
	}

	fmt.Fprintln(logWriter, "Restarting Caddy...")
	if err := ExecuteWithOutput(client, "sudo systemctl reload caddy || sudo systemctl restart caddy", logWriter); err != nil {
		return fmt.Errorf("failed to restart caddy: %w", err)
	}

	fmt.Fprintln(logWriter, "Restarting rathole server...")
	if err := ExecuteWithOutput(client, "sudo systemctl restart rathole-server", logWriter); err != nil {
		return fmt.Errorf("failed to restart rathole: %w", err)
	}

	fmt.Fprintln(logWriter, "=== VPS Deployment Complete ===")
	return nil
}
