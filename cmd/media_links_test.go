package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/media"
)

func TestSignedMediaLinksAreScopedAndSurviveKeyReload(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "fotoro.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key, err := loadMediaSigningKey(database)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadMediaSigningKey(database)
	if err != nil || string(key) != string(reloaded) {
		t.Fatal("media signing key changed across reload")
	}
	if _, err := database.Exec(`UPDATE server_config SET funnel_enabled = 1, public_url = 'https://fotoro.tail123.ts.net' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	file := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(file, []byte("ABCDEFGH"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO media_assets (id, hash, original_name, storage_path, mime_type, preview_status)
		VALUES (?, ?, 'photo.jpg', ?, 'image/jpeg', 'ready')`, id, id, file); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOTORO_STORAGE_DIR", filepath.Join(root, "library"))
	if err := os.MkdirAll(filepath.Join(root, "library"), 0750); err != nil {
		t.Fatal(err)
	}
	service, err := media.NewService(database, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: database, media: service, mediaSigningKey: key}
	assets := []media.Asset{{ID: id, PreviewStatus: "ready"}}
	s.attachDirectMediaLinks(assets)
	if assets[0].DirectContentURL == "" || assets[0].DirectThumbnailURL == "" || assets[0].DirectPreviewURL == "" {
		t.Fatalf("direct media URLs missing: %+v", assets[0])
	}
	request := httptest.NewRequest(http.MethodGet, assets[0].DirectContentURL, nil)
	request.Header.Set("Range", "bytes=0-3")
	response := httptest.NewRecorder()
	s.handleSignedMedia(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "ABCD" {
		t.Fatalf("signed range response: %d %q", response.Code, response.Body.String())
	}
	tampered := httptest.NewRequest(http.MethodGet, strings.Replace(assets[0].DirectContentURL, "/original?", "/medium?", 1), nil)
	response = httptest.NewRecorder()
	s.handleSignedMedia(response, tampered)
	if response.Code != http.StatusForbidden {
		t.Fatalf("tampered variant accepted: %d", response.Code)
	}
	expired := httptest.NewRequest(http.MethodGet, s.signedMediaURL(s.publicMediaBase(), id, "original", time.Now().Add(-time.Minute).Unix()), nil)
	response = httptest.NewRecorder()
	s.handleSignedMedia(response, expired)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expired link accepted: %d", response.Code)
	}
}
