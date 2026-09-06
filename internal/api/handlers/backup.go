package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/service"
)

type BackupHandler struct {
	svc *service.BackupService
}

func NewBackupHandler(svc *service.BackupService) *BackupHandler {
	return &BackupHandler{svc: svc}
}

// GET /api/security/backup/download — runs VACUUM INTO and streams the result
// as a file download with a timestamped filename.
func (h *BackupHandler) Download(w http.ResponseWriter, r *http.Request) {
	snap, err := h.svc.CreateBackup()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	defer snap.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, snap.Filename))
	w.Header().Set("Content-Length", strconv.FormatInt(snap.Size, 10))
	if _, err := io.Copy(w, snap); err != nil {
		// Headers already sent; best we can do is log via the http server.
		// The client will see a truncated download.
		return
	}
}

// POST /api/security/backup/restore — DISABLED for now (route unregistered in
// router.go). Restoring a WAL-mode SQLite DB by renaming the file under the
// live connection is reverted on restart by the old connection's
// checkpoint-on-close via stale -wal/-shm sidecars, so a "successful" restore
// silently loses data. Re-enable only alongside a startup-time swap
// (pending-restore file applied before any connection opens). The handler is
// kept and guarded so re-registering the route without that fix still refuses.
// The upload/validate/swap implementation lives in BackupService.Restore.
func (h *BackupHandler) Restore(w http.ResponseWriter, r *http.Request) {
	response.Error(w, http.StatusNotImplemented, "restore is temporarily disabled in this build")
}
