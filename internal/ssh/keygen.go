package ssh

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

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

// ValidateKeyPair checks that privateKeyPEM (PEM or OpenSSH format) and
// publicAuthorizedKey (authorized_keys line) form a matching pair.
func ValidateKeyPair(privateKeyPEM, publicAuthorizedKey string) error {
	signer, err := ssh.ParsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicAuthorizedKey))
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}
	if !bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
		return fmt.Errorf("private and public keys do not match")
	}
	return nil
}
