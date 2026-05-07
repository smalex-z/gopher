package service

import "testing"

// keydataTokenFromLine handles four authorized_keys line shapes:
//   1. Plain: "ssh-ed25519 AAAA... comment"
//   2. With options: "restrict,permitopen=\"127.0.0.1:*\" ssh-ed25519 AAAA..."
//   3. Blank / comment-only
//   4. Malformed
//
// The function is the heart of the migration logic — without correctly
// matching by type+keydata regardless of options, the upgrade from
// pre-v0.1.0 (no options) to v0.1.0+ (restrict + permitopen) would either
// duplicate keys or scrub them entirely.

func TestKeydataTokenFromLine_PlainKey(t *testing.T) {
	line := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH=== gopher@vps"
	got, ok := keydataTokenFromLine(line)
	if !ok {
		t.Fatalf("expected match, got false")
	}
	want := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH==="
	if got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
}

func TestKeydataTokenFromLine_WithOptions(t *testing.T) {
	line := `restrict,permitopen="127.0.0.1:*",permitopen="localhost:*" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH=== gopher-managed`
	got, ok := keydataTokenFromLine(line)
	if !ok {
		t.Fatalf("expected match, got false")
	}
	want := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH==="
	if got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
}

func TestKeydataTokenFromLine_OptionsWithCommas(t *testing.T) {
	// permitopen list with quoted commas inside — should not terminate the
	// options list before the actual whitespace boundary.
	line := `command="echo hi",permitopen="a:1,b:2",no-pty ssh-rsa AAAAB3NzaC1yc2E= comment`
	got, ok := keydataTokenFromLine(line)
	if !ok {
		t.Fatalf("expected match, got false")
	}
	want := "ssh-rsa AAAAB3NzaC1yc2E="
	if got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
}

func TestKeydataTokenFromLine_Comment(t *testing.T) {
	if _, ok := keydataTokenFromLine("# this is a comment"); ok {
		t.Errorf("comment line should not match")
	}
}

func TestKeydataTokenFromLine_Blank(t *testing.T) {
	if _, ok := keydataTokenFromLine(""); ok {
		t.Errorf("blank line should not match")
	}
	if _, ok := keydataTokenFromLine("   "); ok {
		t.Errorf("whitespace-only line should not match")
	}
}

func TestKeydataTokenFromLine_Malformed(t *testing.T) {
	// Just the type with no key data.
	if _, ok := keydataTokenFromLine("ssh-ed25519"); ok {
		t.Errorf("type-only line should not match")
	}
	// Options without a key body.
	if _, ok := keydataTokenFromLine(`restrict,permitopen="x:1"`); ok {
		t.Errorf("options-only line should not match")
	}
}

func TestAuthorizedKeysLine_NoOptions(t *testing.T) {
	got := authorizedKeysLine("ssh-ed25519 AAAA gopher", "")
	want := "ssh-ed25519 AAAA gopher"
	if got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

func TestAuthorizedKeysLine_WithOptions(t *testing.T) {
	got := authorizedKeysLine("ssh-ed25519 AAAA gopher", jumpboxKeyOptions)
	want := `restrict,permitopen="127.0.0.1:*",permitopen="localhost:*" ssh-ed25519 AAAA gopher`
	if got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}
