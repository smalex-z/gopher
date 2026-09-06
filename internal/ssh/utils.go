package ssh

import (
	"fmt"
	"io"
)

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
