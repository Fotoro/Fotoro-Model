package db

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

const currentSchemaVersion = 6

func (d *DB) migrateVersioned(path string, existing bool) error {
	var version int
	if err := d.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("database schema %d is newer than this Fotoro build", version)
	}
	if version == currentSchemaVersion {
		return nil
	}
	if version >= 1 {
		if err := d.migrateSinceVersion(version); err != nil {
			return err
		}
		return d.recordSchemaVersion()
	}
	if existing {
		if err := d.snapshotBeforeMigration(path); err != nil {
			return err
		}
	}
	if err := d.Migrate(); err != nil {
		return fmt.Errorf("migrate legacy schema: %w", err)
	}
	if err := d.ExtendedMigrate(); err != nil {
		return fmt.Errorf("migrate media schema: %w", err)
	}
	if err := d.migrateSinceVersion(0); err != nil {
		return err
	}
	var unmatched int
	if err := d.QueryRow(`SELECT COUNT(*) FROM images i
		WHERE NOT EXISTS (SELECT 1 FROM media_assets a WHERE a.hash = i.hash)`).Scan(&unmatched); err != nil {
		return fmt.Errorf("reconcile legacy media: %w", err)
	}
	if unmatched != 0 {
		return fmt.Errorf("%d legacy images missing from media_assets", unmatched)
	}
	var integrity string
	if err := d.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("migration integrity check failed: %s: %v", integrity, err)
	}
	return d.recordSchemaVersion()
}

// migrateSinceVersion applies every incremental migration introduced after the
// given stored schema version. All steps are idempotent.
func (d *DB) migrateSinceVersion(version int) error {
	steps := []struct {
		since int
		run   func() error
	}{
		{1, d.migrateDashboardMetrics},
		{2, d.migrateMediaLinkKey},
		{3, d.migrateLocalSecrets},
		{4, d.migrateTieredStorage},
		{4, d.migrateSearchMeta},
		{5, d.migratePackBackup},
	}
	for _, step := range steps {
		if version > step.since {
			continue
		}
		if err := step.run(); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) migrateTieredStorage() error {
	stmts := []string{
		`ALTER TABLE media_assets ADD COLUMN tier TEXT NOT NULL DEFAULT 'hot'`,
		`ALTER TABLE media_assets ADD COLUMN last_accessed_at DATETIME`,
		`ALTER TABLE media_assets ADD COLUMN access_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE media_assets ADD COLUMN archive_path TEXT`,
		`ALTER TABLE media_assets ADD COLUMN archive_codec TEXT`,
		`ALTER TABLE media_assets ADD COLUMN archive_size INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE media_assets ADD COLUMN archive_sha256 TEXT`,
		`ALTER TABLE media_assets ADD COLUMN archived_at DATETIME`,
		`CREATE TABLE IF NOT EXISTS cold_packs (
			id TEXT PRIMARY KEY,
			month TEXT NOT NULL,
			storage_path TEXT NOT NULL,
			member_count INTEGER NOT NULL DEFAULT 0,
			live_bytes INTEGER NOT NULL DEFAULT 0,
			dead_bytes INTEGER NOT NULL DEFAULT 0,
			pack_sha256 TEXT,
			status TEXT NOT NULL DEFAULT 'open',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			sealed_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cold_packs_status ON cold_packs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_cold_packs_month ON cold_packs(month)`,
		`CREATE TABLE IF NOT EXISTS cold_members (
			media_asset_id TEXT PRIMARY KEY REFERENCES media_assets(id) ON DELETE CASCADE,
			pack_id TEXT NOT NULL REFERENCES cold_packs(id),
			member_offset INTEGER NOT NULL,
			member_length INTEGER NOT NULL,
			codec TEXT NOT NULL,
			raw_size INTEGER NOT NULL,
			state TEXT NOT NULL DEFAULT 'live',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cold_members_pack ON cold_members(pack_id)`,
		`CREATE INDEX IF NOT EXISTS idx_media_assets_tier ON media_assets(tier)`,
		`CREATE INDEX IF NOT EXISTS idx_media_assets_access ON media_assets(last_accessed_at)`,
	}
	for _, stmt := range stmts {
		if _, err := d.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") ||
				strings.Contains(err.Error(), "already exists") ||
				strings.Contains(err.Error(), "no such table") {
				continue
			}
			return fmt.Errorf("tiered storage migration: %q: %w", stmt[:50], err)
		}
	}
	return nil
}

func (d *DB) migratePackBackup() error {
	stmts := []string{
		`ALTER TABLE cold_packs ADD COLUMN backup_path TEXT`,
		`ALTER TABLE cold_packs ADD COLUMN backed_up_at DATETIME`,
	}
	for _, stmt := range stmts {
		if _, err := d.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") ||
				strings.Contains(err.Error(), "no such table") {
				continue
			}
			return fmt.Errorf("pack backup migration: %w", err)
		}
	}
	return nil
}

func (d *DB) migrateSearchMeta() error {
	stmts := []string{
		`ALTER TABLE images ADD COLUMN embed_model TEXT`,
		`ALTER TABLE images ADD COLUMN embed_dim INTEGER`,
		`ALTER TABLE images ADD COLUMN embedded_at DATETIME`,
		`CREATE INDEX IF NOT EXISTS idx_images_embedded_at ON images(embedded_at)`,
	}
	for _, stmt := range stmts {
		if _, err := d.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") ||
				strings.Contains(err.Error(), "already exists") ||
				strings.Contains(err.Error(), "no such table") {
				continue
			}
			return fmt.Errorf("search meta migration: %q: %w", stmt[:50], err)
		}
	}
	return nil
}

func (d *DB) migrateLocalSecrets() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS local_secrets (
		name TEXT PRIMARY KEY,
		value BLOB NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create local secrets table: %w", err)
	}
	return nil
}

func (d *DB) migrateMediaLinkKey() error {
	if _, err := d.Exec(`ALTER TABLE server_config ADD COLUMN media_signing_key TEXT`); err != nil {
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return fmt.Errorf("add media signing key: %w", err)
	}
	return nil
}

func (d *DB) recordSchemaVersion() error {
	if _, err := d.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, currentSchemaVersion)); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	return nil
}

func (d *DB) migrateDashboardMetrics() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS dashboard_metrics (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		payload TEXT NOT NULL,
		collected_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create dashboard metrics: %w", err)
	}
	return nil
}

// VACUUM INTO makes a consistent SQLite snapshot, including live WAL changes.
// An interrupted migration keeps its first pre-migration snapshot intact.
func (d *DB) snapshotBeforeMigration(path string) error {
	snapshot := path + ".pre-v1.snapshot"
	if _, err := os.Stat(snapshot); os.IsNotExist(err) {
		if _, err := d.Exec(`VACUUM INTO ?`, snapshot); err != nil {
			return fmt.Errorf("create pre-migration snapshot: %w", err)
		}
	} else if err != nil {
		return err
	}
	copyDB, err := sql.Open("sqlite", "file:"+snapshot+"?mode=ro")
	if err != nil {
		return err
	}
	defer copyDB.Close()
	var integrity string
	if err := copyDB.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("pre-migration snapshot is invalid: %s: %v", integrity, err)
	}
	return nil
}
