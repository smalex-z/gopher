package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Regression for the noise-migration bug that corrupted a machine's client.toml:
// writeFilePreservingMode's open(O_TRUNC) succeeded, then the write hit ENOSPC
// and left the file at 0 bytes. The fix is a statfs pre-flight check that
// returns an error before any destructive op. We can't actually fill the disk
// in CI, but we can ask for an impossibly-large write and confirm (a) it errors
// with the expected message, and (b) the existing file is untouched.
func TestWriteFilePreservingMode_RefusesWhenInsufficientSpace(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/client.toml"
	original := []byte("original content that must not be lost")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// Stub the statfs lookup to simulate a near-full disk. Always returns
	// "100 bytes free" regardless of the directory. Restored after the test.
	prev := availableBytes
	defer func() { availableBytes = prev }()
	availableBytes = func(string) (uint64, bool) { return 100, true }

	err := writeFilePreservingMode(path, []byte("content that needs more than 100 bytes plus 8KiB margin"))
	if err == nil {
		t.Fatal("expected error when statfs reports insufficient space; got nil")
	}
	if !strings.Contains(err.Error(), "insufficient disk space") {
		t.Errorf("expected statfs error, got: %v", err)
	}

	// Confirm original content is intact — this is the load-bearing assertion.
	// Without the precheck this file would now be 0 bytes.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("file corrupted by failed write; got %q, want %q", got, original)
	}
}

func TestWriteFilePreservingMode_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/client.toml"
	if err := os.WriteFile(path, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	want := []byte("new content\nwith multiple lines\n")
	if err := writeFilePreservingMode(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
