package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

func Open(path string) (*DB, error) {
	// WAL mode, busy timeout, foreign keys — all set via connection string
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// SQLite is fastest with a single writer connection
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
			processed_at DATETIME
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_captions USING fts5(path, caption, tags, content='images', content_rowid='id')`,
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
		`CREATE TRIGGER IF NOT EXISTS images_fts_insert AFTER INSERT ON images BEGIN
			INSERT INTO fts_captions(path, caption, tags, rowid) VALUES (new.path, new.caption, new.tags, new.id);
		END`,
		`CREATE TRIGGER IF NOT EXISTS images_fts_delete AFTER DELETE ON images BEGIN
			INSERT INTO fts_captions(fts_captions, rowid, path, caption, tags) VALUES('delete', old.id, old.path, old.caption, old.tags);
		END`,
	}

	for _, stmt := range stmts {
		if _, err := d.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:50], err)
		}
	}
	return nil
}