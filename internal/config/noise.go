package config

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// GenerateNoiseKeypair returns a base64-encoded X25519 keypair compatible
// with rathole's Noise_NK_25519_ChaChaPoly_BLAKE2s transport. The private
// key stays on the rathole server; the public key is distributed to every
// rathole client so it can authenticate the server during handshake.
func GenerateNoiseKeypair() (privB64, pubB64 string, err error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate x25519 key: %w", err)
	}
	privB64 = base64.StdEncoding.EncodeToString(priv.Bytes())
	pubB64 = base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())
	return privB64, pubB64, nil
}
