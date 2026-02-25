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

// NewClientViaJump creates an SSH client to a machine by jumping through the VPS.
// It connects to VPS first, then dials the machine's tunnel port on VPS localhost,
// and authenticates using the VPS-generated SSH keypair installed on the machine.
func NewClientViaJump(vpsHost string, vpsPort int, vpsUsername, vpsPrivateKey string,
machineUsername, machineSSHPrivateKey string, tunnelPort int) (*SSHClient, error) {

vpsSigner, err := ssh.ParsePrivateKey([]byte(vpsPrivateKey))
if err != nil {
return nil, fmt.Errorf("failed to parse VPS private key: %w", err)
}
vpsConfig := &ssh.ClientConfig{
User:            vpsUsername,
Auth:            []ssh.AuthMethod{ssh.PublicKeys(vpsSigner)},
HostKeyCallback: ssh.InsecureIgnoreHostKey(),
Timeout:         30 * time.Second,
}
vpsAddr := net.JoinHostPort(vpsHost, fmt.Sprintf("%d", vpsPort))
vpsClient, err := ssh.Dial("tcp", vpsAddr, vpsConfig)
if err != nil {
return nil, fmt.Errorf("failed to dial VPS: %w", err)
}

tunnelAddr := fmt.Sprintf("localhost:%d", tunnelPort)
tunnelConn, err := vpsClient.Dial("tcp", tunnelAddr)
if err != nil {
vpsClient.Close()
return nil, fmt.Errorf("failed to dial machine tunnel on VPS: %w", err)
}

machineSigner, err := ssh.ParsePrivateKey([]byte(machineSSHPrivateKey))
if err != nil {
tunnelConn.Close()
vpsClient.Close()
return nil, fmt.Errorf("failed to parse machine SSH private key: %w", err)
}
machineConfig := &ssh.ClientConfig{
User:            machineUsername,
Auth:            []ssh.AuthMethod{ssh.PublicKeys(machineSigner)},
HostKeyCallback: ssh.InsecureIgnoreHostKey(),
Timeout:         30 * time.Second,
}
ncc, chans, reqs, err := ssh.NewClientConn(tunnelConn, tunnelAddr, machineConfig)
if err != nil {
tunnelConn.Close()
vpsClient.Close()
return nil, fmt.Errorf("failed to create SSH conn through tunnel: %w", err)
}

return &SSHClient{client: ssh.NewClient(ncc, chans, reqs)}, nil
}
