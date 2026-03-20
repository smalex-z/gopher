package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSafeRemovalPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "reject root", path: "/", wantErr: true},
		{name: "reject dot", path: ".", wantErr: true},
		{name: "reject empty", path: "", wantErr: true},
		{name: "accept data dir", path: "/var/lib/gopher", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureSafeRemovalPath(tt.path)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for path %q", tt.path)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for path %q: %v", tt.path, err)
			}
		})
	}
}

func TestRemoveFileIfExists(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatalf("failed creating file: %v", err)
	}

	removed, err := removeFileIfExists(filePath)
	if err != nil {
		t.Fatalf("unexpected error removing file: %v", err)
	}
	if !removed {
		t.Fatalf("expected removed=true for existing file")
	}

	removed, err = removeFileIfExists(filePath)
	if err != nil {
		t.Fatalf("unexpected error when file already absent: %v", err)
	}
	if removed {
		t.Fatalf("expected removed=false for non-existing file")
	}
}

func TestIsYesResponse(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{input: "y", want: true},
		{input: "Y", want: true},
		{input: "yes", want: true},
		{input: " YES ", want: true},
		{input: "n", want: false},
		{input: "", want: false},
		{input: "no", want: false},
	}

	for _, tt := range tests {
		got := isYesResponse(tt.input)
		if got != tt.want {
			t.Fatalf("isYesResponse(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
