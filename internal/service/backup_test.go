package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// writeThrowawaySQLite creates a valid SQLite file at path with the given
// tables, so we can exercise validateSQLiteBackup's "is this really a
// gopher.db?" probe independently of a live DB.
func writeThrowawaySQLite(t *testing.T, path string, tables ...string) {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, tbl := range tables {
		if err := conn.Exec("CREATE TABLE " + tbl + " (id INTEGER PRIMARY KEY)").Error; err != nil {
			t.Fatalf("create table %s: %v", tbl, err)
		}
	}
	sqlDB, _ := conn.DB()
	_ = sqlDB.Close()
}

func TestValidateSQLiteBackup(t *testing.T) {
	dir := t.TempDir()

	// 1. Random bytes → not a SQLite file → rejected.
	garbage := filepath.Join(dir, "garbage.bin")
	if err := os.WriteFile(garbage, []byte("this is not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateSQLiteBackup(garbage); err == nil {
		t.Error("expected garbage file to be rejected")
	}

	// 2. Valid SQLite but no gopher tables → rejected as "not a gopher.db".
	foreign := filepath.Join(dir, "foreign.db")
	writeThrowawaySQLite(t, foreign, "widgets", "gadgets")
	err := validateSQLiteBackup(foreign)
	if err == nil {
		t.Error("expected foreign sqlite (no gopher tables) to be rejected")
	} else if !strings.Contains(err.Error(), "does not look like a gopher.db") {
		t.Errorf("expected gopher-table probe error, got: %v", err)
	}

	// 3. Valid SQLite with a known gopher table → accepted.
	real := filepath.Join(dir, "real.db")
	writeThrowawaySQLite(t, real, "machines", "tunnels", "app_settings")
	if err := validateSQLiteBackup(real); err != nil {
		t.Errorf("expected a gopher-shaped db to validate, got: %v", err)
	}
}

func TestBackupCreateAndRestoreRoundTrip(t *testing.T) {
	initTestDB(t) // gives us a global db.DB with the real gopher schema (AutoMigrate)

	// CreateBackup snapshots the live (test) DB via VACUUM INTO. dsn only sets
	// where the temp snapshot lands, so point it at a temp dir.
	createSvc := &BackupService{dsn: filepath.Join(t.TempDir(), "src.db")}
	snap, err := createSvc.CreateBackup()
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	defer snap.Close()
	if snap.Size == 0 || !strings.HasPrefix(snap.Filename, "gopher-backup-") {
		t.Fatalf("unexpected snapshot: name=%q size=%d", snap.Filename, snap.Size)
	}

	// Restore that snapshot onto a fresh live-DB path, with the restart stubbed
	// so the real systemctl/os.Exit path never fires in the test.
	liveDB := filepath.Join(t.TempDir(), "gopher.db")
	if err := os.WriteFile(liveDB, []byte("stale placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan struct{}, 1)
	restoreSvc := &BackupService{dsn: liveDB, restart: func() { restarted <- struct{}{} }}

	if err := restoreSvc.Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// The swapped file must now be a valid gopher.db (not the stale placeholder).
	if err := validateSQLiteBackup(liveDB); err != nil {
		t.Errorf("restored file is not a valid gopher.db: %v", err)
	}
	select {
	case <-restarted:
	default:
		t.Error("Restore did not schedule the reload/restart hook")
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	liveDB := filepath.Join(t.TempDir(), "gopher.db")
	if err := os.WriteFile(liveDB, []byte("original good db"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted := false
	svc := &BackupService{dsn: liveDB, restart: func() { restarted = true }}

	err := svc.Restore(strings.NewReader("definitely not a sqlite database"))
	if err == nil {
		t.Fatal("expected Restore to reject a non-sqlite upload")
	}
	// The live DB must be untouched and no restart scheduled on a rejected upload.
	got, _ := os.ReadFile(liveDB)
	if string(got) != "original good db" {
		t.Error("live DB was modified despite invalid upload")
	}
	if restarted {
		t.Error("restart scheduled despite a rejected upload")
	}
}

func TestBackupEmptyDSN(t *testing.T) {
	svc := &BackupService{dsn: ""}
	if _, err := svc.CreateBackup(); err == nil {
		t.Error("CreateBackup with empty dsn should error")
	}
	if err := svc.Restore(strings.NewReader("x")); err == nil {
		t.Error("Restore with empty dsn should error")
	}
}
