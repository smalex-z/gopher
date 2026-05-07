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

func Initialize(dsn string) error {
	var err error
	DB, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Enable foreign key enforcement (SQLite disables it by default).
	if err := DB.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	if err := DB.AutoMigrate(&VPSConfig{}, &Machine{}, &Tunnel{}, &BootstrapToken{}, &MigrationToken{}, &AppSettings{}, &SSHKey{}, &FirewallRule{}, &BotSession{}, &TOTPDevice{}, &HealthCheck{}, &Event{}); err != nil {
		return fmt.Errorf("failed to auto-migrate: %w", err)
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

	// Bootstrap tracking for existing databases: if no migrations are recorded yet
	// but the schema already contains columns introduced by migration 002, the DB
	// was set up by a previous run (via GORM AutoMigrate + untracked SQL migrations).
	// Mark all known migrations as applied so they don't run again.
	var tracked int64
	if err := DB.Raw("SELECT COUNT(*) FROM schema_migrations").Scan(&tracked).Error; err != nil {
		return fmt.Errorf("failed to count tracked migrations: %w", err)
	}
	if tracked == 0 {
		type colInfo struct{ Name string }
		var cols []colInfo
		DB.Raw("PRAGMA table_info(vps_configs)").Scan(&cols)
		for _, col := range cols {
			if col.Name == "ssh_public_key" {
				// Schema is already at 002+ level; record all existing files as applied.
				for _, entry := range entries {
					if entry.IsDir() {
						continue
					}
					DB.Exec("INSERT OR IGNORE INTO schema_migrations (name) VALUES (?)", entry.Name())
				}
				return nil
			}
		}
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
		if err := DB.Exec(string(content)).Error; err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				log.Printf("Migration %s: column already exists, marking as applied", name)
			} else {
				return fmt.Errorf("migration %s failed: %w", name, err)
			}
		}
		if err := DB.Exec("INSERT INTO schema_migrations (name) VALUES (?)", name).Error; err != nil {
			return fmt.Errorf("failed to record migration %s: %w", name, err)
		}
	}
	return nil
}
