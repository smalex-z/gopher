package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// An arch the edge never bundles must 404 regardless of whether this build
// embedded binaries — a deterministic check that doesn't depend on whether
// bin/ has been populated by fetch-deps.
func TestRatholeHandler_404ForUnsupportedArch(t *testing.T) {
	srv := httptest.NewServer(http.StripPrefix("/static/rathole/", ratholeHandler()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/static/rathole/riscv64")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unsupported arch", resp.StatusCode)
	}
}
