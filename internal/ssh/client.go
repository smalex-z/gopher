package ssh

import (
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

type Client struct {
	client *ssh.Client
}

func NewClient(host string, port int, user, privateKey string) (*Client, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return &Client{client: client}, nil
}

func (c *Client) Close() error {
	return c.client.Close()
}

func (c *Client) Run(cmd string) (string, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	out, err := session.CombinedOutput(cmd)
	return string(out), err
}

// WriteFile writes content to a remote file using printf to avoid heredoc issues.
func (c *Client) WriteFile(remotePath, content string) error {
	// Use base64 encoding to safely transfer arbitrary content
	encoded := encodeBase64(content)
	cmd := fmt.Sprintf("echo '%s' | base64 -d > %s", encoded, remotePath)
	_, err := c.Run(cmd)
	return err
}

func encodeBase64(s string) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	b := []byte(s)
	result := make([]byte, 0, ((len(b)+2)/3)*4)
	for i := 0; i < len(b); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], b[i:])
		v := uint(chunk[0])<<16 | uint(chunk[1])<<8 | uint(chunk[2])
		result = append(result, chars[(v>>18)&0x3F], chars[(v>>12)&0x3F])
		if n > 1 {
			result = append(result, chars[(v>>6)&0x3F])
		} else {
			result = append(result, '=')
		}
		if n > 2 {
			result = append(result, chars[v&0x3F])
		} else {
			result = append(result, '=')
		}
	}
	return string(result)
}
