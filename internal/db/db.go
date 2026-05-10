package db

import (
	"embed"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed migrations/*.sql
var migrations embed.FS

var DB *gorm.DB

// applyPragmas appends per-connection pragmas to the DSN. Foreign-key
// enforcement is OFF in the DSN: AutoMigrate sometimes rebuilds a table by
// CREATE-temp / copy / DROP / RENAME, and the DROP fails with
// SQLITE_CONSTRAINT_FOREIGNKEY (787) when other tables still reference it.
// We enable FKs on the single pooled connection AFTER migrations complete —
// see the PRAGMA toggle in Initialize.
func applyPragmas(dsn string) string {
	const params = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	if strings.Contains(dsn, "?") {
		return dsn + "&" + params
	}
	return dsn + "?" + params
}

func Initialize(dsn string) error {
	var err error
	DB, err = gorm.Open(sqlite.Open(applyPragmas(dsn)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// SQLite is single-writer and PRAGMAs are connection-scoped. Pinning
	// the pool to a single connection lets us turn `foreign_keys` ON
	// after migrations and have the setting stick for every subsequent
	// query — without this, queries borrowed from a different pooled
	// connection would silently lose FK enforcement and ON DELETE CASCADE
	// on tunnels / the bootstrap_tokens.machine_id reference would not
	// fire. For Gopher's dashboard-scale workload, serializing on one
	// connection is invisible.
	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("failed to access underlying *sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	// FKs OFF during AutoMigrate — see applyPragmas comment.
	if err := DB.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		return fmt.Errorf("failed to disable foreign_keys for migration: %w", err)
	}

	if err := DB.AutoMigrate(&VPSConfig{}, &Machine{}, &Tunnel{}, &BootstrapToken{}, &MigrationToken{}, &AppSettings{}, &SSHKey{}, &FirewallRule{}, &BotSession{}, &TOTPDevice{}, &HealthCheck{}, &Event{}); err != nil {
		return fmt.Errorf("failed to auto-migrate: %w", err)
	}

	// Partial unique indexes for port columns. AutoMigrate's `gorm:"uniqueIndex"`
	// tag would treat the zero-value (unallocated) as a real value and reject
	// every machine after the first one — so we hand-roll partial indexes
	// scoped to non-zero ports. Together with the retry loop in
	// bootstrap.Register, this turns the concurrent-allocation race into a
	// constraint failure the caller can simply re-pick from.
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_machines_tunnel_port_unique ON machines(tunnel_port) WHERE tunnel_port > 0`).Error; err != nil {
		return fmt.Errorf("failed to create tunnel_port unique index: %w", err)
	}
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_machines_agent_remote_port_unique ON machines(agent_remote_port) WHERE agent_remote_port > 0`).Error; err != nil {
		return fmt.Errorf("failed to create agent_remote_port unique index: %w", err)
	}
	// Same shape for tunnels.subdomain and tunnels.rathole_port. AutoMigrate
	// runs before the SQL migration that originally declared these columns
	// UNIQUE, so the SQL CREATE TABLE is a no-op (table already exists from
	// the struct) and the constraint never lands on fresh installs. Empty
	// subdomain is legal — multiple tunnels can route by raw port without a
	// subdomain — so the partial-index `WHERE` clause is critical.
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_tunnels_subdomain_unique ON tunnels(subdomain) WHERE subdomain <> ''`).Error; err != nil {
		return fmt.Errorf("failed to create tunnels.subdomain unique index: %w", err)
	}
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_tunnels_rathole_port_unique ON tunnels(rathole_port) WHERE rathole_port > 0`).Error; err != nil {
		return fmt.Errorf("failed to create tunnels.rathole_port unique index: %w", err)
	}

	if err := runMigrations(); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	if err := migrateSSHKeysFromSettings(); err != nil {
		log.Printf("WARN: SSH key migration: %v", err)
	}

	if err := migrateTOTPSecretToDevice(); err != nil {
		log.Printf("WARN: TOTP device migration: %v", err)
	}

	// Migrations done — turn FK enforcement ON for the rest of the process.
	// Because SetMaxOpenConns(1) above pins the pool to a single connection,
	// this PRAGMA persists for every subsequent query.
	if err := DB.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return fmt.Errorf("failed to enable foreign_keys: %w", err)
	}
	var fk int
	if err := DB.Raw("PRAGMA foreign_keys").Scan(&fk).Error; err != nil {
		return fmt.Errorf("failed to verify foreign_keys pragma: %w", err)
	}
	if fk != 1 {
		return fmt.Errorf("foreign_keys pragma did not stick (got %d, want 1)", fk)
	}

	return nil
}

// migrateTOTPSecretToDevice copies the legacy single-device TOTP secret stored
// on AppSettings into a row in the new totp_devices table. Runs once: no-op
// after the first successful migration (or if 2FA was never enabled).
func migrateTOTPSecretToDevice() error {
	// If the new table already has rows, migration already happened.
	var existing int64
	if err := DB.Model(&TOTPDevice{}).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	settings, err := GetSettings()
	if err != nil || !settings.TOTPEnabled || settings.TOTPSecret == "" {
		return nil // nothing to migrate
	}

	device := &TOTPDevice{
		ID:        "migrated-default",
		Name:      "Original device",
		Secret:    settings.TOTPSecret,
		CreatedAt: time.Now(),
	}
	if err := DB.Create(device).Error; err != nil {
		return fmt.Errorf("create totp_device: %w", err)
	}
	// Clear the pending-enrollment slot on AppSettings so a fresh enrollment
	// can use it without colliding with the migrated secret.
	settings.TOTPSecret = ""
	if err := DB.Save(settings).Error; err != nil {
		return fmt.Errorf("clear migrated secret: %w", err)
	}
	log.Printf("Migrated TOTP secret from app_settings to totp_devices table")
	return nil
}

// migrateSSHKeysFromSettings copies any SSH keypair stored in app_settings into
// the new ssh_keys table and assigns it to all existing machines.
func migrateSSHKeysFromSettings() error {
	count, err := CountSSHKeys()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil // already migrated
	}

	// Fresh installs never had the legacy ssh_public_key/ssh_private_key
	// columns on app_settings — those were removed when the dedicated
	// ssh_keys table landed. Probe the schema first so we don't trip GORM's
	// warn-level logger with a "no such column" SQL error on every fresh
	// install.
	type colInfo struct{ Name string }
	var cols []colInfo
	if err := DB.Raw("PRAGMA table_info(app_settings)").Scan(&cols).Error; err != nil {
		return nil // table doesn't exist yet — no legacy data to migrate
	}
	hasLegacyCols := false
	for _, c := range cols {
		if c.Name == "ssh_public_key" {
			hasLegacyCols = true
			break
		}
	}
	if !hasLegacyCols {
		return nil
	}

	// Read raw values directly from the DB column to avoid depending on the struct field.
	var row struct {
		PubKey  string `gorm:"column:ssh_public_key"`
		PrivKey string `gorm:"column:ssh_private_key"`
	}
	if err := DB.Raw("SELECT ssh_public_key, ssh_private_key FROM app_settings WHERE id = 'singleton' LIMIT 1").Scan(&row).Error; err != nil || row.PubKey == "" {
		return nil // nothing to migrate
	}

	key := &SSHKey{
		ID:         "migrated-default",
		Name:       "Default",
		PublicKey:  row.PubKey,
		PrivateKey: row.PrivKey,
		IsDefault:  true,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := DB.Create(key).Error; err != nil {
		return fmt.Errorf("create ssh_key: %w", err)
	}
	// Assign to all existing machines.
	_ = DB.Model(&Machine{}).Where("ssh_key_id = '' OR ssh_key_id IS NULL").Update("ssh_key_id", key.ID).Error
	log.Printf("Migrated SSH key from app_settings to ssh_keys table")
	return nil
}

// runMigrations executes every numbered .sql migration in lexical order,
// recording each as applied in schema_migrations.
//
// Every migration must be idempotent — re-running on an already-converged
// schema must succeed. CREATE TABLE / CREATE INDEX statements use
// `IF NOT EXISTS`; UPDATE backfills include WHERE clauses that filter out
// already-converged rows; data INSERTs use `INSERT OR IGNORE`. ALTER TABLE
// ADD COLUMN can't currently express IF NOT EXISTS, so isDuplicateColumnErr
// catches the column-already-added case (which legitimately happens on
// fresh installs where AutoMigrate has already provisioned the column from
// the model struct).
//
// The previous implementation tried to short-circuit re-runs by detecting
// "the schema looks like it's at 002+ level, mark everything applied." That
// heuristic fired on every fresh install because AutoMigrate added the
// detected column from the VPSConfig struct, silently skipping data
// migrations like 012_unified_events.sql for any operator upgrading an old
// AutoMigrate-only DB. Idempotent re-runs are simpler and safer.
func runMigrations() error {
	if err := DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT (datetime('now'))
	)`).Error; err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		var count int64
		if err := DB.Raw("SELECT COUNT(*) FROM schema_migrations WHERE name = ?", name).Scan(&count).Error; err != nil {
			return fmt.Errorf("failed to check migration %s: %w", name, err)
		}
		if count > 0 {
			continue
		}

		content, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		log.Printf("Running migration: %s", name)
		// Wrap every migration in a transaction so a multi-statement file
		// either lands entirely or rolls back. Without this, statement N
		// committing and statement N+1 erroring leaves the schema in a
		// half-migrated state — and on the next boot statement N's effect
		// (e.g. ADD COLUMN) bites the retry path with "column already
		// exists" against migrations the runner can't safely skip.
		txErr := DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(string(content)).Error; err != nil {
				if isDuplicateColumnErr(err) {
					// AutoMigrate already added the column from the model
					// struct; the SQL ALTER TABLE is now redundant. Record
					// as applied so we don't keep retrying every boot.
					log.Printf("Migration %s: column already added by AutoMigrate, marking as applied", name)
					return nil
				}
				return err
			}
			return nil
		})
		if txErr != nil {
			return fmt.Errorf("migration %s failed: %w", name, txErr)
		}
		if err := DB.Exec("INSERT INTO schema_migrations (name) VALUES (?)", name).Error; err != nil {
			return fmt.Errorf("failed to record migration %s: %w", name, err)
		}
	}
	return nil
}

// isDuplicateColumnErr matches SQLite's error string for ALTER TABLE ADD
// COLUMN against an already-existing column. Drivers wrap the SQLite error
// differently — modernc-go reports
// `SQL logic error: duplicate column name: foo (1)` — so we match on the
// distinctive `duplicate column name:` substring (with the trailing colon
// to guard against unrelated errors that happen to mention the bare
// phrase).
func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "duplicate column name:")
}
