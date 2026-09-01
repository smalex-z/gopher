package service

// Minimal minisign signature verification for release artifacts, implemented
// against the published format (https://jedisct1.github.io/minisign/) with
// stdlib ed25519 + x/crypto blake2b — no new module dependency for a
// security-critical path. Only verification lives here; signing happens
// offline with the minisign CLI (scripts/sign-release.sh).

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

const (
	// minisign wire sizes: 2-byte algorithm tag, 8-byte random key ID,
	// ed25519 public key / signature.
	msAlgLen   = 2
	msKeyIDLen = 8
	msPubLen   = msAlgLen + msKeyIDLen + ed25519.PublicKeySize  // 42
	msSigLen   = msAlgLen + msKeyIDLen + ed25519.SignatureSize  // 74
)

// minisignPubKey is the decoded form of the base64 public-key line.
type minisignPubKey struct {
	keyID [msKeyIDLen]byte
	key   ed25519.PublicKey
}

func parseMinisignPubKey(b64 string) (*minisignPubKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("public key is not valid base64: %w", err)
	}
	if len(raw) != msPubLen {
		return nil, fmt.Errorf("public key has %d bytes, want %d", len(raw), msPubLen)
	}
	if string(raw[:msAlgLen]) != "Ed" {
		return nil, fmt.Errorf("unsupported public key algorithm %q", raw[:msAlgLen])
	}
	pk := &minisignPubKey{key: ed25519.PublicKey(raw[msAlgLen+msKeyIDLen:])}
	copy(pk.keyID[:], raw[msAlgLen:msAlgLen+msKeyIDLen])
	return pk, nil
}

// verifyMinisignSignature checks a .minisig file (sigFile) over message
// against the given base64 public key. It verifies BOTH signatures the format
// carries: the file signature (over the message — prehashed with blake2b-512
// for the modern "ED" algorithm, raw for legacy "Ed") and the global
// signature (over file-signature || trusted comment), so the trusted comment
// can't be swapped either. Returns nil only on a full match with the same key
// ID the public key declares.
func verifyMinisignSignature(pubKeyB64 string, message, sigFile []byte) error {
	pub, err := parseMinisignPubKey(pubKeyB64)
	if err != nil {
		return err
	}

	// Signature file layout:
	//   untrusted comment: <ignored>
	//   base64(alg[2] || key_id[8] || signature[64])
	//   trusted comment: <tc>
	//   base64(global_signature[64])   — ed25519 over signature || tc
	var sigB64, trustedComment, globalB64 string
	for _, line := range strings.Split(string(sigFile), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "untrusted comment:"):
			// informational only — never authenticated, never trusted
		case strings.HasPrefix(line, "trusted comment: "):
			trustedComment = strings.TrimPrefix(line, "trusted comment: ")
		case strings.TrimSpace(line) == "":
			// skip blanks
		case sigB64 == "":
			sigB64 = line
		case globalB64 == "":
			globalB64 = line
		}
	}
	if sigB64 == "" || globalB64 == "" {
		return fmt.Errorf("malformed signature file: missing signature lines")
	}

	rawSig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("signature is not valid base64: %w", err)
	}
	if len(rawSig) != msSigLen {
		return fmt.Errorf("signature has %d bytes, want %d", len(rawSig), msSigLen)
	}
	alg := string(rawSig[:msAlgLen])
	if !bytes.Equal(rawSig[msAlgLen:msAlgLen+msKeyIDLen], pub.keyID[:]) {
		return fmt.Errorf("signature was made with a different key ID than the trusted public key")
	}
	sig := rawSig[msAlgLen+msKeyIDLen:]

	var signed []byte
	switch alg {
	case "ED": // prehashed (minisign default since 0.6)
		h := blake2b.Sum512(message)
		signed = h[:]
	case "Ed": // legacy pure ed25519 over the raw message
		signed = message
	default:
		return fmt.Errorf("unsupported signature algorithm %q", alg)
	}
	if !ed25519.Verify(pub.key, signed, sig) {
		return fmt.Errorf("signature does not verify — file was not signed by the trusted key, or was modified")
	}

	globalSig, err := base64.StdEncoding.DecodeString(globalB64)
	if err != nil {
		return fmt.Errorf("global signature is not valid base64: %w", err)
	}
	if len(globalSig) != ed25519.SignatureSize {
		return fmt.Errorf("global signature has %d bytes, want %d", len(globalSig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub.key, append(append([]byte{}, sig...), []byte(trustedComment)...), globalSig) {
		return fmt.Errorf("trusted-comment signature does not verify")
	}
	return nil
}
