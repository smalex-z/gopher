package service

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/smalex-z/gopher/internal/db"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// BackupService creates point-in-time snapshots of gopher.db and restores from them.
//
// Backups use SQLite's VACUUM INTO, which produces a consistent, defragmented copy
// without locking writers. Restore validates the uploaded file, atomically swaps it
// into place, and triggers a service restart so the new DB takes effect.
type BackupService struct {
	dsn string // path to the live SQLite file (e.g. /var/lib/gopher/gopher.db)
}

func NewBackupService(dsn string) *BackupService {
	return &BackupService{dsn: dsn}
}

// BackupSnapshot is a one-shot reader over a backup file. Closing it removes
// the temp file from disk; callers must always Close.
type BackupSnapshot struct {
	Filename string
	Size     int64
	file     *os.File
	path     string
}

func (b *BackupSnapshot) Read(p []byte) (int, error) { return b.file.Read(p) }
func (b *BackupSnapshot) Close() error {
	err := b.file.Close()
	_ = os.Remove(b.path)
	return err
}

// CreateBackup runs VACUUM INTO and returns a snapshot the caller can stream.
// The caller must Close the snapshot — this removes the temp file.
func (s *BackupService) CreateBackup() (*BackupSnapshot, error) {
	if s.dsn == "" {
		return nil, errors.New("backup: no database path configured")
	}

	// VACUUM INTO refuses to overwrite existing files, so generate a unique temp name.
	tmpPath := fmt.Sprintf("%s.backup-%d.tmp", s.dsn, time.Now().UnixNano())
	if err := db.DB.Exec("VACUUM INTO ?", tmpPath).Error; err != nil {
		return nil, fmt.Errorf("VACUUM INTO failed: %w", err)
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("stat snapshot: %w", err)
	}

	filename := fmt.Sprintf("gopher-backup-%s.db", time.Now().UTC().Format("20060102-150405"))
	return &BackupSnapshot{Filename: filename, Size: stat.Size(), file: f, path: tmpPath}, nil
}

// Restore validates the uploaded SQLite file, swaps it onto the live DB path,
// and schedules a service restart so the running process picks it up.
func (s *BackupService) Restore(r io.Reader) error {
	if s.dsn == "" {
		return errors.New("restore: no database path configured")
	}

	tmpPath := fmt.Sprintf("%s.restore-%d.tmp", s.dsn, time.Now().UnixNano())
	tmpFile, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmpPath) }

	// Limit the upload to 256 MiB. A real Gopher DB is well under 100 KiB even
	// with thousands of tunnels, so anything bigger is suspicious.
	const maxUpload = 256 << 20
	written, err := io.Copy(tmpFile, io.LimitReader(r, maxUpload+1))
	if err != nil {
		tmpFile.Close()
		cleanup()
		return fmt.Errorf("write upload: %w", err)
	}
	if written > maxUpload {
		tmpFile.Close()
		cleanup()
		return fmt.Errorf("upload too large (>%d bytes)", maxUpload)
	}
	if err := tmpFile.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := validateSQLiteBackup(tmpPath); err != nil {
		cleanup()
		return fmt.Errorf("invalid backup: %w", err)
	}

	// Atomically replace the live DB. On Linux, rename(2) on the same filesystem
	// is atomic and the running GORM connection keeps the old inode until close,
	// so in-flight queries finish cleanly.
	if err := os.Rename(tmpPath, s.dsn); err != nil {
		cleanup()
		return fmt.Errorf("swap db file: %w", err)
	}

	// Schedule restart after a short delay so the HTTP response can flush.
	// gopher.service is configured with Restart=always, so systemd brings us
	// back up automatically.
	go func() {
		time.Sleep(750 * time.Millisecond)
		args := append(append([]string{}, privilegedCmdPrefix()...), "systemctl", "restart", "gopher")
		if err := exec.Command(args[0], args[1:]...).Run(); err != nil {
			log.Printf("backup restore: systemctl restart failed: %v — exiting so systemd restarts us", err)
			os.Exit(0)
		}
	}()

	return nil
}

// validateSQLiteBackup opens the candidate file and runs a sanity check:
// integrity_check must return "ok", and at least one of the core Gopher tables
// must exist. This catches "user uploaded a random file" and corrupted backups.
func validateSQLiteBackup(path string) error {
	conn, err := gorm.Open(sqlite.Open(path+"?mode=ro"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("open as sqlite: %w", err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer sqlDB.Close()

	var integrity string
	if err := conn.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("integrity_check returned %q", integrity)
	}

	// Look for at least one table we know belongs to Gopher.
	var count int64
	if err := conn.Raw(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name IN ('machines','tunnels','app_settings')
	`).Scan(&count).Error; err != nil {
		return fmt.Errorf("schema probe: %w", err)
	}
	if count == 0 {
		return errors.New("file does not look like a gopher.db (no machines/tunnels/app_settings tables)")
	}
	return nil
}
