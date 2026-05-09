package main

import (
	"embed"
	"io/fs"
	"net/http"
)

// agentsFS holds the gopher-agent binaries built by scripts/build.sh.
// Served at /static/agents/<filename> so the bootstrap script and the
// existing-machine migration tool can curl them straight from the VPS.
//
// The directory is created (with a .gitkeep) before compile time so go:embed
// doesn't fail on developer machines that haven't run build.sh yet.
//
//go:embed all:agents
var agentsFS embed.FS

// agentsHandler returns an http.Handler that serves embedded agent binaries.
// The handler 404s for any name not built into the binary.
func agentsHandler() http.Handler {
	sub, err := fs.Sub(agentsFS, "agents")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "agent binaries unavailable", http.StatusInternalServerError)
		})
	}
	fileSrv := http.FileServer(http.FS(sub))
	// Force download (not inline display) and lock the content type so browsers
	// don't sniff the binary as something weird.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		fileSrv.ServeHTTP(w, r)
	})
}
