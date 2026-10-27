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

func TestVersionOneAddsDashboardMetricsWithoutChangingMedia(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fotoro.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO media_assets (id, hash, original_name, storage_path)
		VALUES (?, ?, 'photo.jpg', '/photo.jpg')`, strings.Repeat("a", 64), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE dashboard_metrics`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count, version int
	if err := database.QueryRow(`SELECT COUNT(*) FROM media_assets`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("media count = %d, error = %v", count, err)
	}
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("version = %d, error = %v", version, err)
	}
	if _, err := database.Exec(`INSERT INTO dashboard_metrics (id, payload, collected_at) VALUES (1, '{}', 1)`); err != nil {
		t.Fatalf("metrics table missing: %v", err)
	}
}
