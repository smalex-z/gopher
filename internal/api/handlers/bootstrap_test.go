package handlers

import (
	"strings"
	"testing"
)

func TestGenerateBootstrapScript_HeadlessSupport(t *testing.T) {
	script := generateBootstrapScript("https://router.example.com")

	// The script must work non-interactively in two ways:
	//   1. Honor GOPHER_MACHINE_NAME / GOPHER_SSH_USER env vars when set.
	//   2. Fall back to the box's own hostname when neither env var nor
	//      a usable /dev/tty is present (instead of exiting). The
	//      "auto-derived" hint is what callers see in the SSH output and
	//      tells us the headless fallback path is in place.
	checks := []string{
		"GOPHER_MACHINE_NAME",
		"GOPHER_SSH_USER",
		"auto-derived",
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
