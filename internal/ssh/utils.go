package ssh

import (
	"fmt"
	"io"
)

func UploadFile(client *SSHClient, localContent []byte, remotePath string) error {
	if err := client.UploadFile(localContent, remotePath); err != nil {
		return fmt.Errorf("failed to upload %s: %w", remotePath, err)
	}
	return nil
}

func ExecuteWithOutput(client *SSHClient, cmd string, w io.Writer) error {
	fmt.Fprintf(w, "$ %s\n", cmd)
	return client.ExecuteWithOutput(cmd, w)
}

func ExecuteCommands(client *SSHClient, cmds []string, w io.Writer) error {
	for _, cmd := range cmds {
		if err := ExecuteWithOutput(client, cmd, w); err != nil {
			return fmt.Errorf("command failed [%s]: %w", cmd, err)
		}
	}
	return nil
}
