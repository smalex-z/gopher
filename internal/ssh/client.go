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

// executeTimeout bounds a single short remote command. ClientConfig.Timeout
// only covers the TCP dial, NOT session.Run — a live-but-hung origin (or one
// reachable only through a wedged rathole tunnel) would otherwise block the
// caller forever. Every Execute caller runs a quick status/probe command, and
// several are on HTTP request goroutines (machine status, network-info, ssh-key
// reassign, recover), so an unbounded Run there is a handler-wedge waiting to
// happen. ExecuteWithOutput is deliberately left unbounded — it streams a
// long-running client install to the deploy log and legitimately runs for
// minutes.
const executeTimeout = 20 * time.Second

func (c *SSHClient) Execute(cmd string) (string, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1) // buffered so the goroutine never leaks
	go func() { done <- session.Run(cmd) }()

	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("command failed: %w (stderr: %s)", err, stderr.String())
		}
		return stdout.String(), nil
	case <-time.After(executeTimeout):
		_ = session.Close() // unblock the pending session.Run
		return "", fmt.Errorf("ssh command timed out after %s: %s", executeTimeout, cmd)
	}
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
//
// IMPORTANT: this REPLACES the destination's inode (rename via mv). For files
// watched by an inotify-based hot reloader (rathole client.toml in
// particular), use UploadFileSudoInPlace instead — otherwise the watcher
// loses its subscription on first push and never sees subsequent changes
// without a process restart, which drops every connected tunnel.
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

// UploadFileSudoInPlace uploads content and writes it to remotePath while
// preserving the file's inode. Used for rathole client.toml: rathole 0.5's
// notify watcher subscribes to the inode, and rename(2)-based replacement
// silently breaks the subscription so subsequent config pushes never get
// hot-reloaded.
//
// The strategy is the same two-step (SFTP to tmp, then privilege-escalate to
// install) as UploadFileSudo, but the second step uses `cat tmp > target`
// via `sudo tee` instead of `mv`. tee opens with O_TRUNC|O_WRONLY|O_CREATE,
// which keeps the inode and fires IN_MODIFY for the watcher.
//
// If the SSH user already owns the destination, falls through to the
// direct SFTP path: sftpClient.Create truncates by default and preserves
// the inode.
func (c *SSHClient) UploadFileSudoInPlace(content []byte, remotePath, owner string) error {
	if err := c.UploadFile(content, remotePath); err == nil {
		return nil
	}
	tmpPath := fmt.Sprintf("/tmp/.gopher-%d", time.Now().UnixNano())
	if err := c.UploadFile(content, tmpPath); err != nil {
		return fmt.Errorf("failed to upload temporary file for sudo install: %w", err)
	}
	// `sudo tee` truncates+rewrites in place — same inode as before, which
	// keeps rathole's notify watcher subscribed. Discard tee's stdout so
	// it doesn't echo the file content back over the SSH session.
	//
	// Pre-flight a free-space check: tee opens with O_TRUNC, so a disk-full
	// write would leave client.toml truncated and the notify watcher would
	// hot-reload a broken config, dropping every tunnel on the machine (the
	// corruption the agent path's statfs guard prevents). Fail-open if df can't
	// be read — this is defense, not a hard gate.
	neededKB := len(content)/1024 + 64
	cmd := fmt.Sprintf(
		`avail=$(df -Pk "$(dirname %q)" 2>/dev/null | awk 'NR==2{print $4}'); `+
			`if [ "${avail:-999999999}" -lt %d ]; then echo "insufficient disk space (${avail}KB free, need %dKB)" >&2; exit 28; fi; `+
			`sudo tee %q < %q > /dev/null`,
		remotePath, neededKB, neededKB, remotePath, tmpPath)
	if owner != "" {
		cmd += fmt.Sprintf(" && sudo chown %q %q", owner, remotePath)
	}
	cmd += fmt.Sprintf(" ; rm -f %q", tmpPath)
	if _, err := c.Execute(cmd); err != nil {
		_, _ = c.Execute(fmt.Sprintf("rm -f %q", tmpPath))
		return fmt.Errorf("in-place install of %s failed: %w", remotePath, err)
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

