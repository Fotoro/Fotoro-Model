package db

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

const currentSchemaVersion = 3

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
	if version == 2 {
		if err := d.migrateMediaLinkKey(); err != nil {
			return err
		}
		return d.recordSchemaVersion()
	}
	if version == 1 {
		if err := d.migrateDashboardMetrics(); err != nil {
			return err
		}
		if err := d.migrateMediaLinkKey(); err != nil {
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
	if err := d.migrateDashboardMetrics(); err != nil {
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
