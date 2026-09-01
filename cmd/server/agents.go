package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
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

// computeAgentHashes returns arch tag → hex sha256 for every embedded
// gopher-agent binary (gopher-agent-linux-<arch>). Published to
// internal/agentdist at startup so the self-update trigger and the
// bootstrap/migrate script renderers can hand out authoritative checksums
// over authenticated channels. Empty map on dev builds with no staged agents.
func computeAgentHashes() map[string]string {
	const prefix = "gopher-agent-linux-"
	out := map[string]string{}
	entries, err := agentsFS.ReadDir("agents")
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || strings.HasSuffix(name, ".sha256") {
			continue
		}
		data, err := agentsFS.ReadFile("agents/" + name)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		out[strings.TrimPrefix(name, prefix)] = hex.EncodeToString(sum[:])
	}
	return out
}

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
		// Serve a "<name>.sha256" sidecar for any embedded binary, computed on
		// the fly. The agent self-updater fetches this to verify its download
		// against corruption before installing.
		if name := strings.TrimSuffix(r.URL.Path, ".sha256"); name != r.URL.Path {
			data, err := fs.ReadFile(sub, strings.TrimPrefix(name, "/"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			sum := sha256.Sum256(data)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), strings.TrimPrefix(name, "/"))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		fileSrv.ServeHTTP(w, r)
	})
}
