package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		if _, err := database.Exec(`INSERT INTO media_assets (id, hash, original_name, storage_path, preview_status, file_size)
			VALUES (?, ?, 'photo.jpg', '/photo.jpg', 'ready', 5)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	insert(strings.Repeat("a", 64))
	if _, err := database.Exec(`INSERT INTO images (path, hash, has_faces) VALUES ('/photo.jpg', ?, 1)`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO mobile_devices (device_id, device_name, is_active) VALUES ('phone-1', 'Phone', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO analysis_jobs (media_asset_id, job_type, status) VALUES (?, 'thumbnail', 'pending')`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: database, media: service, cacheDir: filepath.Join(root, ".cache"), model: "test-model"}
	first, err := s.dashboardMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 1 || first.Processed != 1 || first.ThumbnailsMedium != 1 || first.StorageUsedBytes != 12 || first.PhotosWithFaces != 1 || first.DevicesCount != 2 || first.QueuePending != 1 || first.ConfiguredModel != "test-model" || first.CollectedAt == "" {
		t.Fatalf("unexpected first snapshot: %+v", first)
	}
	var stored string
	if err := database.QueryRow(`SELECT payload FROM dashboard_metrics WHERE id = 1`).Scan(&stored); err != nil || !strings.Contains(stored, `"total":1`) {
		t.Fatalf("snapshot not persisted: %q, %v", stored, err)
	}
	response := httptest.NewRecorder()
	s.handleStats(response, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("stats response: HTTP %d, cache %q", response.Code, response.Header().Get("Cache-Control"))
	}
	var served DashboardMetrics
	if err := json.Unmarshal(response.Body.Bytes(), &served); err != nil || served.Total != 1 {
		t.Fatalf("stats endpoint returned %+v, %v", served, err)
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

func TestGalleryDeviceCountsComeFromDeviceAssets(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	id := strings.Repeat("c", 64)
	if _, err := database.Exec(`INSERT INTO media_assets (id, hash, original_name, storage_path) VALUES (?, ?, 'photo.jpg', '/photo.jpg')`, id, id); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO mobile_devices (device_id, device_name, platform, last_seen, is_active)
		VALUES ('phone-1', 'My phone', 'android', CURRENT_TIMESTAMP, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO device_assets (device_id, device_asset_id, media_asset_id)
		VALUES ('phone-1', 'camera-1', ?)`, id); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: database}
	response := httptest.NewRecorder()
	s.handleGalleryDevices(response, httptest.NewRequest(http.MethodGet, "/devices", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("devices response: HTTP %d", response.Code)
	}
	var body struct {
		Devices []struct {
			ID    string `json:"id"`
			Items string `json:"items"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Devices) != 2 || body.Devices[1].ID != "phone-1" || body.Devices[1].Items != "1 item" {
		t.Fatalf("device inventory = %+v, error = %v", body.Devices, err)
	}
}
