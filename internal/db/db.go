package db

import (
"embed"
"fmt"
"log"

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

if err := DB.AutoMigrate(&VPSConfig{}, &Machine{}, &Tunnel{}); err != nil {
return fmt.Errorf("failed to auto-migrate: %w", err)
}

if err := runMigrations(); err != nil {
return fmt.Errorf("failed to run migrations: %w", err)
}

return nil
}

func runMigrations() error {
entries, err := migrations.ReadDir("migrations")
if err != nil {
return err
}
for _, entry := range entries {
if entry.IsDir() {
continue
}
content, err := migrations.ReadFile("migrations/" + entry.Name())
if err != nil {
return err
}
log.Printf("Running migration: %s", entry.Name())
if err := DB.Exec(string(content)).Error; err != nil {
log.Printf("Migration %s note: %v", entry.Name(), err)
}
}
return nil
}
