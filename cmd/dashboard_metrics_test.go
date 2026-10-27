package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fotoro/internal/db"
	"fotoro/internal/media"
)

func TestDashboardMetricsPersistAndRefresh(t *testing.T) {
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
	service, err := media.NewService(database, filepath.Join(root, "fotoro.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".cache", "thumbnails", "medium", "aa"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".cache", "thumbnails", "medium", "aa", "a.jpg"), []byte("preview"), 0640); err != nil {
		t.Fatal(err)
	}
	insert := func(id string) {
		t.Helper()
		if _, err := database.Exec(`INSERT INTO media_assets (id, hash, original_name, storage_path, preview_status)
			VALUES (?, ?, 'photo.jpg', '/photo.jpg', 'ready')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	insert(strings.Repeat("a", 64))
	s := &Server{db: database, media: service, cacheDir: filepath.Join(root, ".cache"), model: "test-model"}
	first, err := s.dashboardMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 1 || first.Processed != 1 || first.ThumbnailsMedium != 1 || first.ConfiguredModel != "test-model" || first.CollectedAt == "" {
		t.Fatalf("unexpected first snapshot: %+v", first)
	}
	var stored string
	if err := database.QueryRow(`SELECT payload FROM dashboard_metrics WHERE id = 1`).Scan(&stored); err != nil || !strings.Contains(stored, `"total":1`) {
		t.Fatalf("snapshot not persisted: %q, %v", stored, err)
	}
	insert(strings.Repeat("b", 64))
	cached, err := s.dashboardMetrics(context.Background())
	if err != nil || cached.Total != 1 {
		t.Fatalf("fresh snapshot unexpectedly rescanned: %+v, %v", cached, err)
	}
	if _, err := database.Exec(`UPDATE dashboard_metrics SET collected_at = 0 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	updated, err := s.dashboardMetrics(context.Background())
	if err != nil || updated.Total != 2 {
		t.Fatalf("stale snapshot not refreshed: %+v, %v", updated, err)
	}
}
