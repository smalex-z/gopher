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

// POST /api/security/backup/restore — accepts a multipart upload "file" containing
// a SQLite backup, validates it, swaps it onto the live DB path, and schedules
// a service restart.
func (h *BackupHandler) Restore(w http.ResponseWriter, r *http.Request) {
	const maxMem = 16 << 20 // 16 MiB in memory before spilling to disk
	if err := r.ParseMultipartForm(maxMem); err != nil {
		response.BadRequest(w, "invalid multipart payload")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		response.BadRequest(w, "missing 'file' field")
		return
	}
	defer file.Close()

	if err := h.svc.Restore(file); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, map[string]string{"status": "restored", "message": "Database replaced. Service restarting..."})
}
