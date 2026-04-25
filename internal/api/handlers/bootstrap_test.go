package handlers

import (
	"strings"
	"testing"
)

func TestGenerateBootstrapScript_HeadlessSupport(t *testing.T) {
	script := generateBootstrapScript("https://router.example.com")

	checks := []string{
		"GOPHER_MACHINE_NAME",
		"GOPHER_SSH_USER",
		"non-interactive use",
	}
	for _, needle := range checks {
		if !strings.Contains(script, needle) {
			t.Fatalf("expected bootstrap script to contain %q", needle)
		}
	}

	removed := []string{
		"Continue and overwrite? [y/N]:",
		"Aborted by user",
	}
	for _, needle := range removed {
		if strings.Contains(script, needle) {
			t.Fatalf("bootstrap script should not contain interactive overwrite prompt %q", needle)
		}
	}
}
