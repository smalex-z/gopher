package service

// End-to-end tests for Apply()'s release-signing policy. These exist because
// build.ReleaseSigningPubKey is empty in every shipped build so far — without
// injecting a key here, every enforcement branch in verifyReleaseSignature is
// dead code whose first real execution would be on a production edge.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"

	"github.com/smalex-z/gopher/internal/build"
)

// testMinisigner produces genuine minisign-format signatures from an
// in-memory ed25519 key, so the signing policy is testable end to end without
// the minisign CLI. The wire format it emits is pinned to real CLI output by
// the vectors in minisign_test.go.
type testMinisigner struct {
	pub  string
	priv ed25519.PrivateKey
	kid  [8]byte
}

func newTestMinisigner(t *testing.T) *testMinisigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s := &testMinisigner{priv: priv}
	if _, err := rand.Read(s.kid[:]); err != nil {
		t.Fatalf("generate key id: %v", err)
	}
	raw := append(append([]byte("Ed"), s.kid[:]...), pub...)
	s.pub = base64.StdEncoding.EncodeToString(raw)
	return s
}

func (s *testMinisigner) sign(message []byte, trustedComment string) string {
	h := blake2b.Sum512(message)
	sig := ed25519.Sign(s.priv, h[:])
	sigLine := base64.StdEncoding.EncodeToString(append(append([]byte("ED"), s.kid[:]...), sig...))
	global := ed25519.Sign(s.priv, append(append([]byte{}, sig...), []byte(trustedComment)...))
	return "untrusted comment: signed for test\n" + sigLine +
		"\ntrusted comment: " + trustedComment + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n"
}

func setSigningKey(t *testing.T, pub string) {
	t.Helper()
	orig := build.ReleaseSigningPubKey
	build.ReleaseSigningPubKey = pub
	t.Cleanup(func() { build.ReleaseSigningPubKey = orig })
}

// sumsFor returns a SHA256SUMS.txt body for the fake binary in the same
// two-column, dist/-prefixed layout release.yml's sha256sum step emits.
func sumsFor(binary []byte) string {
	sum := sha256.Sum256(binary)
	return hex.EncodeToString(sum[:]) + "  dist/" + releaseAssetName() + "\n"
}

func stubInstallMustNotRun(t *testing.T, why string) {
	t.Helper()
	origInstall := installVerifiedBinary
	installVerifiedBinary = func(tmpPath string) error {
		t.Errorf("installVerifiedBinary must not run: %s", why)
		os.Remove(tmpPath)
		return nil
	}
	t.Cleanup(func() { installVerifiedBinary = origInstall })
}

func TestUpdateApply_KeySetUnsignedStableRefused(t *testing.T) {
	initTestDB(t)
	setSigningKey(t, newTestMinisigner(t).pub)
	binary := []byte("new gopher binary")
	srv := startFakeGitHub(t, "v0.2.0", false, binary, sumsFor(binary)) // no .minisig asset
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "stable")
	stubInstallMustNotRun(t, "the stable release is unsigned")

	err := NewUpdateService().Apply()
	if err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("Apply = %v, want unsigned-stable refusal", err)
	}
}

// A beta/alpha edge still installs stable releases (releaseMatchesChannel
// includes them), and the modeled attacker controls asset presence — so a
// stable release with its signature stripped must be refused on EVERY
// channel, not just "stable".
func TestUpdateApply_SignatureStrippedStableRefusedOnBetaChannel(t *testing.T) {
	initTestDB(t)
	setSigningKey(t, newTestMinisigner(t).pub)
	binary := []byte("new gopher binary")
	srv := startFakeGitHub(t, "v0.2.0", false, binary, sumsFor(binary))
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "beta")
	stubInstallMustNotRun(t, "a stable release with a stripped signature must not install on beta")

	err := NewUpdateService().Apply()
	if err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("Apply = %v, want unsigned-stable refusal on the beta channel", err)
	}
}

// The channel string is user/DB input: releaseMatchesChannel treats an
// unrecognized value as stable, so the signature requirement must key on the
// release, not the channel label — otherwise "Stable", "prod", or a future
// channel silently drops the strictest protection.
func TestUpdateApply_UnknownChannelStillRequiresSignature(t *testing.T) {
	initTestDB(t)
	setSigningKey(t, newTestMinisigner(t).pub)
	binary := []byte("new gopher binary")
	srv := startFakeGitHub(t, "v0.2.0", false, binary, sumsFor(binary))
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "Stable")
	stubInstallMustNotRun(t, "an unknown channel must not bypass the signature requirement")

	err := NewUpdateService().Apply()
	if err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("Apply = %v, want unsigned refusal under an unknown channel", err)
	}
}

func TestUpdateApply_SignatureFromWrongKeyRefused(t *testing.T) {
	initTestDB(t)
	setSigningKey(t, newTestMinisigner(t).pub)
	imposter := newTestMinisigner(t)
	binary := []byte("new gopher binary")
	sums := sumsFor(binary)
	srv := startFakeGitHub(t, "v0.2.0", false, binary, sums, imposter.sign([]byte(sums), "gopher release v0.2.0"))
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "stable")
	stubInstallMustNotRun(t, "the signature is from the wrong key")

	err := NewUpdateService().Apply()
	if err == nil || !strings.Contains(err.Error(), "signature verification FAILED") {
		t.Fatalf("Apply = %v, want signature-verification failure", err)
	}
}

// A present-but-invalid signature must fail even on a prerelease — only a
// MISSING signature is tolerated there.
func TestUpdateApply_InvalidSignatureRefusedOnPrerelease(t *testing.T) {
	initTestDB(t)
	signer := newTestMinisigner(t)
	setSigningKey(t, signer.pub)
	binary := []byte("new gopher binary")
	sums := sumsFor(binary)
	sig := signer.sign([]byte(sums+"tampered\n"), "gopher release v0.2.0-beta.1") // signs different content
	srv := startFakeGitHub(t, "v0.2.0-beta.1", true, binary, sums, sig)
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "beta")
	stubInstallMustNotRun(t, "the prerelease signature does not match the sums content")

	err := NewUpdateService().Apply()
	if err == nil || !strings.Contains(err.Error(), "signature verification FAILED") {
		t.Fatalf("Apply = %v, want signature-verification failure on prerelease", err)
	}
}

// The trusted comment binds a signature to its release tag. A still-valid
// signature copied verbatim from an older release (with its sums+binary)
// under a new tag must be refused — otherwise repo write access converts a
// rollback into an apparent upgrade, bypassing both signing and isNewer.
func TestUpdateApply_ReplayedSignatureForOtherTagRefused(t *testing.T) {
	initTestDB(t)
	signer := newTestMinisigner(t)
	setSigningKey(t, signer.pub)
	oldBinary := []byte("old vulnerable binary")
	oldSums := sumsFor(oldBinary)
	oldSig := signer.sign([]byte(oldSums), "gopher release v0.1.5")
	// Attacker publishes "v9.9.9" carrying the old release's assets verbatim.
	srv := startFakeGitHub(t, "v9.9.9", false, oldBinary, oldSums, oldSig)
	pointUpdatesAt(t, srv, "v0.2.0")
	setChannel(t, "stable")
	stubInstallMustNotRun(t, "the signature names a different release")

	err := NewUpdateService().Apply()
	if err == nil || !strings.Contains(err.Error(), "signature names") {
		t.Fatalf("Apply = %v, want trusted-comment/tag mismatch refusal", err)
	}
}

func TestUpdateApply_SignedStableInstalls(t *testing.T) {
	initTestDB(t)
	signer := newTestMinisigner(t)
	setSigningKey(t, signer.pub)
	binary := []byte("new gopher binary")
	sums := sumsFor(binary)
	srv := startFakeGitHub(t, "v0.2.0", false, binary, sums, signer.sign([]byte(sums), "gopher release v0.2.0"))
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "stable")

	installed := false
	origInstall := installVerifiedBinary
	installVerifiedBinary = func(tmpPath string) error {
		installed = true
		return os.Remove(tmpPath)
	}
	t.Cleanup(func() { installVerifiedBinary = origInstall })

	if err := NewUpdateService().Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !installed {
		t.Fatal("installVerifiedBinary was never invoked for a correctly signed release")
	}
}

// Prereleases keep dev velocity: with the key set, a MISSING signature is
// tolerated (checksum-only) when the selected release is a prerelease.
func TestUpdateApply_UnsignedPrereleaseTolerated(t *testing.T) {
	initTestDB(t)
	setSigningKey(t, newTestMinisigner(t).pub)
	binary := []byte("new gopher beta binary")
	srv := startFakeGitHub(t, "v0.2.0-beta.1", true, binary, sumsFor(binary)) // no .minisig
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "beta")

	installed := false
	origInstall := installVerifiedBinary
	installVerifiedBinary = func(tmpPath string) error {
		installed = true
		return os.Remove(tmpPath)
	}
	t.Cleanup(func() { installVerifiedBinary = origInstall })

	if err := NewUpdateService().Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !installed {
		t.Fatal("installVerifiedBinary was never invoked for an unsigned prerelease")
	}
}
