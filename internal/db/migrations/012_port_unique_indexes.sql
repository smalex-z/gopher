-- Partial unique indexes on tunnel ports were removed: SQLite partial indexes
-- (WHERE clause) are not tracked by GORM's AutoMigrate, which causes it to
-- attempt a table recreation on every startup, failing due to FK constraints.
-- Port collision prevention is handled at the application layer instead.
SELECT 1;
