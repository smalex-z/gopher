package ssh

import (
_ "embed"
"fmt"
"io"
)

//go:embed templates/client-install.sh
var clientInstallScript string

//go:embed templates/rathole-client.service
var ratholeClientService string

func DeployClient(client *SSHClient, machineID string, config string, logWriter io.Writer) error {
fmt.Fprintln(logWriter, "=== Deploying Rathole Client ===")

fmt.Fprintln(logWriter, "Step 1: Installing rathole...")
if err := client.UploadFile([]byte(clientInstallScript), "/tmp/client-install.sh"); err != nil {
return fmt.Errorf("failed to upload install script: %w", err)
}
if err := ExecuteWithOutput(client, "chmod +x /tmp/client-install.sh && /tmp/client-install.sh", logWriter); err != nil {
return fmt.Errorf("failed to install rathole client: %w", err)
}

fmt.Fprintln(logWriter, "Step 2: Writing rathole client config...")
if err := client.UploadFile([]byte(config), "/etc/rathole/client.toml"); err != nil {
return fmt.Errorf("failed to upload client config: %w", err)
}

fmt.Fprintln(logWriter, "Step 3: Installing systemd service...")
if err := client.UploadFile([]byte(ratholeClientService), "/etc/systemd/system/rathole-client.service"); err != nil {
return fmt.Errorf("failed to upload service file: %w", err)
}

fmt.Fprintln(logWriter, "Step 4: Enabling and starting service...")
cmds := []string{
"systemctl daemon-reload",
"systemctl enable rathole-client",
"systemctl restart rathole-client",
}
if err := ExecuteCommands(client, cmds, logWriter); err != nil {
return fmt.Errorf("failed to enable service: %w", err)
}

fmt.Fprintln(logWriter, "=== Client Deployment Complete ===")
return nil
}
