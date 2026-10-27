package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fotoro/internal/db"
)

func TestReconcileMediaSizesRepairsOnlyMissingCatalogSizes(t *testing.T) {
	root := t.TempDir()
	database, err := db.Open(filepath.Join(root, "fotoro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	t.Setenv("FOTORO_STORAGE_DIR", filepath.Join(root, "library"))
	if err := os.MkdirAll(filepath.Join(root, "library"), 0750); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(database, filepath.Join(root, "fotoro.db"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "legacy.jpg")
	if err := os.WriteFile(file, []byte("sixteen bytes!!!"), 0640); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("b", 64)
	if _, err := database.Exec(`INSERT INTO media_assets (id, hash, original_name, storage_path, file_size)
		VALUES (?, ?, 'legacy.jpg', ?, 0)`, id, id, file); err != nil {
		t.Fatal(err)
	}
	updated, err := service.ReconcileMediaSizes(context.Background(), 100)
	if err != nil || updated != 1 {
		t.Fatalf("reconciled %d: %v", updated, err)
	}
	var size int64
	if err := database.QueryRow(`SELECT file_size FROM media_assets WHERE id = ?`, id).Scan(&size); err != nil || size != 16 {
		t.Fatalf("catalog size = %d, %v", size, err)
	}
	again, err := service.ReconcileMediaSizes(context.Background(), 100)
	if err != nil || again != 0 {
		t.Fatalf("repeated reconciliation changed %d rows: %v", again, err)
	}
}
