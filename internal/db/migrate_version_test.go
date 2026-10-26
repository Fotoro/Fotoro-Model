package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyMigrationCreatesRestorableSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE images (
		id INTEGER PRIMARY KEY, path TEXT NOT NULL, hash TEXT UNIQUE NOT NULL,
		caption TEXT, category TEXT, tags TEXT, has_text INTEGER, has_faces INTEGER,
		orientation TEXT, tier TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		processed_at DATETIME);
		INSERT INTO images (path, hash, category) VALUES ('legacy.jpg', ?, 'photo')`, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	legacy.Close()

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version, count int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version = %d, error = %v", version, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM media_assets`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("media count = %d, error = %v", count, err)
	}
	snapshot := path + ".pre-v1.snapshot"
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("pre-migration snapshot missing: %v", err)
	}
	copyDB, err := sql.Open("sqlite", "file:"+snapshot+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	if err := copyDB.QueryRow(`SELECT COUNT(*) FROM images`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("snapshot media count = %d, error = %v", count, err)
	}
}
