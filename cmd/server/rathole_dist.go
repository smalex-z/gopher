package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/smalex-z/gopher/internal/embedbin"
)

// ratholeHandler serves the bundled rathole binary matching a bootstrapping
// origin's `uname -m`, so origins fetch it from the edge over TLS instead of
// downloading a zip from GitHub. Served at /static/rathole/<uname> (e.g.
// .../x86_64, .../aarch64, .../armv7l). A "<uname>.sha256" sidecar is computed
// on the fly so the origin can verify the download before installing — same
// pattern as the agent binaries.
//
// 404s when this build has no embedded binaries (dev edge) or for an arch the
// edge doesn't bundle.
func ratholeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		wantSum := strings.HasSuffix(name, ".sha256")
		uname := strings.TrimSuffix(name, ".sha256")

		data, ok := embedbin.RatholeForOrigin(uname)
		if !ok {
			http.Error(w, "rathole unavailable for "+uname, http.StatusNotFound)
			return
		}
		if wantSum {
			sum := sha256.Sum256(data)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "\n"))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="rathole"`)
		_, _ = w.Write(data)
	})
}
