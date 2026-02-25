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
HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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
