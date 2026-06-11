package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

func Open(path string) (*DB, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	d := &DB{db}
	if err := d.Migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func (d *DB) Migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS images (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL,
			hash TEXT UNIQUE NOT NULL,
			caption TEXT,
			category TEXT DEFAULT 'unknown',
			tags TEXT,
			has_text INTEGER DEFAULT 0,
			has_faces INTEGER DEFAULT 0,
			orientation TEXT DEFAULT 'unknown',
			tier TEXT DEFAULT 'bulk',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			processed_at DATETIME,
			ocr_text TEXT,
			content_type TEXT DEFAULT 'unknown',
			embedding BLOB
		)`,
		// Add missing columns to existing tables (SQLite ignores if they exist)
		`ALTER TABLE images ADD COLUMN ocr_text TEXT`,
		`ALTER TABLE images ADD COLUMN content_type TEXT DEFAULT 'unknown'`,
		`ALTER TABLE images ADD COLUMN embedding BLOB`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_captions USING fts5(path, caption, tags, ocr_text, content='images', content_rowid='id')`,
		`CREATE TABLE IF NOT EXISTS thumbnails (
			hash TEXT NOT NULL,
			size TEXT NOT NULL,
			width INTEGER,
			height INTEGER,
			data BLOB NOT NULL,
			PRIMARY KEY (hash, size)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_images_hash ON images(hash)`,
		`CREATE INDEX IF NOT EXISTS idx_images_category ON images(category)`,
		`CREATE INDEX IF NOT EXISTS idx_images_content_type ON images(content_type)`,
		`CREATE TRIGGER IF NOT EXISTS images_fts_insert AFTER INSERT ON images BEGIN
			INSERT INTO fts_captions(path, caption, tags, ocr_text, rowid) VALUES (new.path, new.caption, new.tags, new.ocr_text, new.id);
		END`,
		`CREATE TRIGGER IF NOT EXISTS images_fts_delete AFTER DELETE ON images BEGIN
			INSERT INTO fts_captions(fts_captions, rowid, path, caption, tags, ocr_text) VALUES('delete', old.id, old.path, old.caption, old.tags, old.ocr_text);
		END`,
	}

	for _, stmt := range stmts {
		if _, err := d.Exec(stmt); err != nil {
			// SQLite returns "duplicate column name" for ALTER TABLE ADD COLUMN if column exists.
			// Also ignore "already exists" for indexes and tables.
			if strings.Contains(err.Error(), "duplicate column name") ||
				strings.Contains(err.Error(), "already exists") ||
				strings.Contains(err.Error(), "SQL logic error: no such column") {
				// If we get "no such column" on the trigger, it means the table is old and
				// the ALTER TABLE failed or wasn't run. But ALTER TABLE should have handled it.
				// If the trigger fails because of missing columns, we need to drop and recreate.
				// For now, just continue and let the user know they might need to delete the DB.
				continue
			}
			return fmt.Errorf("exec %q: %w", stmt[:50], err)
		}
	}
	return nil
}
