package ssh

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"

	"golang.org/x/crypto/ssh"
)

// GenerateRSAKeypair returns (privateKeyPEM, publicKeyAuthorizedKeys, error)
func GenerateRSAKeypair() (string, string, error) {
	privKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return "", "", err
	}
	privDER := x509.MarshalPKCS1PrivateKey(privKey)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER})

	pub, err := ssh.NewPublicKey(&privKey.PublicKey)
	if err != nil {
		return "", "", err
	}
	pubKey := string(ssh.MarshalAuthorizedKey(pub))
	return string(privPEM), pubKey, nil
}
