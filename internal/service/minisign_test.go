package service

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// Vectors generated with the real minisign CLI (0.11):
//
//	minisign -G -W -p test.pub -s test.key -c "gopher test key"
//	minisign -S -W -s test.key -m SHA256SUMS.txt -t "gopher release test"
//
// so the parser is exercised against genuine wire format, not our own
// serialization assumptions.
const (
	msTestPubKey = "RWT0fyi630IpmqmiTRIph44BLYsaKnitki4FRjL4wAcBoh+vpnde3WYo"
	msOtherPub   = "RWRkvripHLyKWyMhgi5f8/unReSowL0OW+o/DiJR7bzXDNSQ0gI9/ggA"

	msTestMessage = "deadbeef  gopher-linux-amd64\ncafebabe  gopher-linux-arm64\n"

	msTestSig = `untrusted comment: signature from minisign secret key
RUT0fyi630IpmliQgwe3IJDYB/SZrI3ioPvLupANO4ckwAklI2Y1usLAORS0fH2b1i5k1WcWIV0aE18e11YDdH68dZr5AgSENQk=
trusted comment: gopher release test
Fyjb/EDpA2lTNwLBhr8BmRsAYSv/JtWa5dd0jpXfTxc2rZfT0Jz3albcZybyyGK3brOqjem94bHEDX9gQ8h4AQ==
`
)

func TestVerifyMinisign_Valid(t *testing.T) {
	tc, err := verifyMinisignSignature(msTestPubKey, []byte(msTestMessage), []byte(msTestSig))
	if err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if tc != "gopher release test" {
		t.Errorf("trusted comment = %q, want the comment the vector was signed with", tc)
	}
}

func TestVerifyMinisign_TamperedMessage(t *testing.T) {
	tampered := strings.Replace(msTestMessage, "deadbeef", "attacker0", 1)
	if _, err := verifyMinisignSignature(msTestPubKey, []byte(tampered), []byte(msTestSig)); err == nil {
		t.Fatal("tampered message accepted")
	}
}

func TestVerifyMinisign_WrongKey(t *testing.T) {
	if _, err := verifyMinisignSignature(msOtherPub, []byte(msTestMessage), []byte(msTestSig)); err == nil {
		t.Fatal("signature accepted under a different public key")
	}
}

func TestVerifyMinisign_TamperedTrustedComment(t *testing.T) {
	swapped := strings.Replace(msTestSig, "trusted comment: gopher release test", "trusted comment: attacker note", 1)
	if _, err := verifyMinisignSignature(msTestPubKey, []byte(msTestMessage), []byte(swapped)); err == nil {
		t.Fatal("tampered trusted comment accepted")
	}
}

// The 2-byte algorithm tag is covered by neither signature in the minisign
// format, so a verifier that accepts the legacy pure-ed25519 "Ed" algorithm
// lets an attacker downgrade a genuine "ED" (prehashed) signature and validly
// verify a message the key holder never signed: the raw blake2b-512 hash of
// the original message. Pin the rejection of exactly that construction.
func TestVerifyMinisign_LegacyAlgDowngradeRejected(t *testing.T) {
	lines := strings.Split(msTestSig, "\n")
	raw, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		t.Fatalf("decode vector signature line: %v", err)
	}
	raw[0], raw[1] = 'E', 'd' // "ED" → legacy "Ed"
	lines[1] = base64.StdEncoding.EncodeToString(raw)
	downgraded := strings.Join(lines, "\n")

	h := blake2b.Sum512([]byte(msTestMessage)) // the message the downgrade would "verify"
	if _, err := verifyMinisignSignature(msTestPubKey, h[:], []byte(downgraded)); err == nil {
		t.Fatal("legacy 'Ed' downgrade accepted a message the key never signed")
	}
}

func TestVerifyMinisign_Malformed(t *testing.T) {
	for name, sig := range map[string]string{
		"empty":       "",
		"comments":    "untrusted comment: x\n",
		"junk-base64": "untrusted comment: x\n!!!notbase64!!!\ntrusted comment: y\nAAAA\n",
	} {
		if _, err := verifyMinisignSignature(msTestPubKey, []byte(msTestMessage), []byte(sig)); err == nil {
			t.Fatalf("%s: malformed signature accepted", name)
		}
	}
	if _, err := verifyMinisignSignature("not-a-key", []byte(msTestMessage), []byte(msTestSig)); err == nil {
		t.Fatal("malformed public key accepted")
	}
}
