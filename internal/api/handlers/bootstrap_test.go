package handlers

import (
	"strings"
	"testing"
)

func TestGenerateBootstrapScript_WarnsBeforeOverwrite(t *testing.T) {
	script := generateBootstrapScript("https://router.example.com")

	checks := []string{
		"existing rathole",
		"Continue and overwrite? [y/N]:",
		"Aborted by user",
	}
	for _, needle := range checks {
		if !strings.Contains(script, needle) {
			t.Fatalf("expected bootstrap script to contain %q", needle)
		}
	}
}
