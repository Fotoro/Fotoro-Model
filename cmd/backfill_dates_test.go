package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fotoro/internal/db"
)

func TestBackfillCaptureDatesDryRunAndPreservedFileTime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fotoro.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	photo := filepath.Join(root, "legacy.jpg")
	if err := os.WriteFile(photo, []byte("legacy data without EXIF"), 0600); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2022, 7, 9, 11, 12, 0, 0, time.UTC)
	if err := os.Chtimes(photo, want, want); err != nil {
		t.Fatal(err)
	}
	imported := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if _, err := database.Exec(`INSERT INTO media_assets
		(id, hash, original_name, storage_path, media_kind, imported_at)
		VALUES ('legacy-1', 'legacy-1', 'legacy.jpg', ?, 'image', ?)`, photo, imported); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO images (path, hash) VALUES (?, 'legacy-1')`, photo); err != nil {
		t.Fatal(err)
	}
	if err := RunBackfillCaptureDates(path, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM media_assets WHERE captured_at IS NOT NULL`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("dry run changed a date: count=%d err=%v", count, err)
	}
	if err := RunBackfillCaptureDates(path, false); err != nil {
		t.Fatal(err)
	}
	var captured, legacy time.Time
	if err := database.QueryRow(`SELECT captured_at FROM media_assets WHERE id = 'legacy-1'`).Scan(&captured); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT taken_at FROM images WHERE hash = 'legacy-1'`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if !captured.Equal(want) || !legacy.Equal(want) {
		t.Fatalf("capture dates = %v, %v; want %v", captured, legacy, want)
	}
}
