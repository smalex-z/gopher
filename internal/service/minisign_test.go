package service

import (
	"strings"
	"testing"
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
	if err := verifyMinisignSignature(msTestPubKey, []byte(msTestMessage), []byte(msTestSig)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyMinisign_TamperedMessage(t *testing.T) {
	tampered := strings.Replace(msTestMessage, "deadbeef", "attacker0", 1)
	if err := verifyMinisignSignature(msTestPubKey, []byte(tampered), []byte(msTestSig)); err == nil {
		t.Fatal("tampered message accepted")
	}
}

func TestVerifyMinisign_WrongKey(t *testing.T) {
	if err := verifyMinisignSignature(msOtherPub, []byte(msTestMessage), []byte(msTestSig)); err == nil {
		t.Fatal("signature accepted under a different public key")
	}
}

func TestVerifyMinisign_TamperedTrustedComment(t *testing.T) {
	swapped := strings.Replace(msTestSig, "trusted comment: gopher release test", "trusted comment: attacker note", 1)
	if err := verifyMinisignSignature(msTestPubKey, []byte(msTestMessage), []byte(swapped)); err == nil {
		t.Fatal("tampered trusted comment accepted")
	}
}

func TestVerifyMinisign_Malformed(t *testing.T) {
	for name, sig := range map[string]string{
		"empty":       "",
		"comments":    "untrusted comment: x\n",
		"junk-base64": "untrusted comment: x\n!!!notbase64!!!\ntrusted comment: y\nAAAA\n",
	} {
		if err := verifyMinisignSignature(msTestPubKey, []byte(msTestMessage), []byte(sig)); err == nil {
			t.Fatalf("%s: malformed signature accepted", name)
		}
	}
	if err := verifyMinisignSignature("not-a-key", []byte(msTestMessage), []byte(msTestSig)); err == nil {
		t.Fatal("malformed public key accepted")
	}
}
