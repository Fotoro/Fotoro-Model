package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fotoro/internal/auth"
	"fotoro/internal/db"
	"fotoro/internal/media"
)

func TestDeviceStatusRepairsCaptureDateWithoutReupload(t *testing.T) {
	root := t.TempDir()
	database, err := db.Open(filepath.Join(root, "fotoro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service, err := media.NewService(database, filepath.Join(root, "fotoro.db"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "video.mp4")
	if err := os.WriteFile(source, []byte("test video bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	imported, err := service.ImportFile(source, media.ImportOptions{
		OriginalName: "video.mp4", MIMEType: "video/mp4", DeviceID: "phone-1", DeviceAssetID: "asset-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	captured := time.Date(2022, 3, 12, 14, 15, 0, 0, time.UTC)
	body, _ := json.Marshal(map[string]any{"assets": []map[string]any{{
		"device_asset_id": "asset-1", "size": imported.Asset.FileSize, "captured_at": captured.Format(time.RFC3339),
	}}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/device-assets/status", bytes.NewReader(body))
	request = request.WithContext(auth.WithUser(request.Context(), &auth.User{ID: "device:phone-1"}))
	response := httptest.NewRecorder()
	s := &Server{db: database, media: service}
	s.handleDeviceAssetStatus(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status update: HTTP %d: %s", response.Code, response.Body.String())
	}
	got, err := service.GetAsset(imported.Asset.ID)
	if err != nil || got.CapturedAt == nil || !got.CapturedAt.Equal(captured) {
		t.Fatalf("capture date not saved: %v, asset=%+v", err, got)
	}
	page, total, err := service.ListAssets(1, 10, "", "", "2022-03")
	if err != nil || total != 1 || len(page) != 1 || page[0].ID != imported.Asset.ID {
		t.Fatalf("timeline month not updated: total=%d err=%v", total, err)
	}
}
