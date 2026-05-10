package ssh

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SSHClient struct {
	client *ssh.Client
}

// NewClient dials an SSH endpoint with the supplied private key. Host-key
// verification is intentionally disabled because every caller dials a
// rathole-tunnel localhost port (or a bind_ip on the VPS itself) — there is
// no public network leg to MITM. An attacker positioned to hijack
// localhost/VPS-bound traffic already has the root-equivalent access that
// host-key pinning would normally protect against, so verification adds no
// real defense in our deployment shape (single-tenant VPS).
//
// On a multi-tenant or shared host this would no longer hold; tracked
// against #9 in the pre-stable audit.
func NewClient(host string, port int, username, privateKey string) (*SSHClient, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // see godoc — only safe because every dial is localhost/VPS-bound
		Timeout:         30 * time.Second,
	}

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("failed to dial SSH: %w", err)
	}

	return &SSHClient{client: client}, nil
}

func (c *SSHClient) Close() error {
	return c.client.Close()
}

func (c *SSHClient) Execute(cmd string) (string, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(cmd); err != nil {
		return "", fmt.Errorf("command failed: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}

func (c *SSHClient) ExecuteWithOutput(cmd string, w io.Writer) error {
	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	session.Stdout = w
	session.Stderr = w

	return session.Run(cmd)
}
func (c *SSHClient) ReadFile(remotePath string) ([]byte, error) {
	sftpClient, err := sftp.NewClient(c.client)
	if err != nil {
		return nil, fmt.Errorf("failed to create SFTP client: %w", err)
	}
	defer sftpClient.Close()

	f, err := sftpClient.Open(remotePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(f)
}
func (c *SSHClient) UploadFile(content []byte, remotePath string) error {
	sftpClient, err := sftp.NewClient(c.client)
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}
	defer sftpClient.Close()

	_ = sftpClient.MkdirAll(dirName(remotePath))

	f, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("failed to create remote file: %w", err)
	}
	defer f.Close()

	_, err = f.Write(content)
	return err
}

// UploadFileSudo uploads content to remotePath. It first attempts a direct SFTP
// write (succeeds when the SSH user owns the file/directory). If that is denied,
// it uploads to a temp path the user can always write and uses "sudo mv" to
// install it. Pass owner="" to skip the chown step after the move.
func (c *SSHClient) UploadFileSudo(content []byte, remotePath, owner string) error {
	if err := c.UploadFile(content, remotePath); err == nil {
		return nil
	}
	tmpPath := fmt.Sprintf("/tmp/.gopher-%d", time.Now().UnixNano())
	if err := c.UploadFile(content, tmpPath); err != nil {
		return fmt.Errorf("failed to upload temporary file for sudo install: %w", err)
	}
	cmd := fmt.Sprintf("sudo mv %q %q", tmpPath, remotePath)
	if owner != "" {
		cmd += fmt.Sprintf(" && sudo chown %q %q", owner, remotePath)
	}
	if _, err := c.Execute(cmd); err != nil {
		_, _ = c.Execute(fmt.Sprintf("rm -f %q", tmpPath))
		return fmt.Errorf("permission denied writing %s; sudo fallback also failed: %w", remotePath, err)
	}
	return nil
}

func (c *SSHClient) UploadReader(r io.Reader, remotePath string) error {
	sftpClient, err := sftp.NewClient(c.client)
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}
	defer sftpClient.Close()

	_ = sftpClient.MkdirAll(dirName(remotePath))

	f, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("failed to create remote file: %w", err)
	}
	defer f.Close()

	_, err = io.Copy(f, r)
	return err
}

func dirName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

