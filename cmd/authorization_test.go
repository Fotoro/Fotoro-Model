package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"fotoro/internal/auth"
	"fotoro/internal/db"
)

func TestOwnerAndDeviceAccess(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`UPDATE server_config SET auth_user_id = 'owner-1' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO mobile_devices (device_id, device_name, is_active)
		VALUES ('phone-1', 'Phone', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO device_credentials
		(device_id, owner_id, token_hash, name, platform, scopes)
		VALUES ('phone-1', 'owner-1', 'digest', 'Phone', 'android', 'media:read,media:write')`); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: database}
	cases := []struct {
		name, method, path, user string
		allowed                  bool
	}{
		{"owner gallery", "GET", "/api/images", "owner-1", true},
		{"foreign account", "GET", "/api/images", "owner-2", false},
		{"device list", "GET", "/api/v1/media", "device:phone-1", true},
		{"device upload", "POST", "/api/v1/uploads", "device:phone-1", true},
		{"device cannot pair", "POST", "/api/v1/pairing/sessions", "device:phone-1", false},
		{"device cannot change server", "POST", "/api/server/stop", "device:phone-1", false},
		{"device cannot use legacy gallery", "GET", "/api/images", "device:phone-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			err := s.authorizeUser(r, &auth.User{ID: tc.user})
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}

func TestDesktopSessionTokenStaysLoopbackOnly(t *testing.T) {
	s := &Server{authUser: &auth.User{ID: "owner-1"}, authAccessToken: "desktop-token"}
	handler := s.localOnly(s.handleAuthSession)
	local := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/auth/session", nil)
	local.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	handler(w, local)
	if w.Code != http.StatusOK {
		t.Fatalf("local desktop session: HTTP %d", w.Code)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload["access_token"] != "desktop-token" {
		t.Fatalf("desktop token missing: %v", err)
	}
	remote := httptest.NewRequest(http.MethodGet, "https://node.example/api/auth/session", nil)
	remote.RemoteAddr = "100.100.100.100:12345"
	w = httptest.NewRecorder()
	handler(w, remote)
	if w.Code != http.StatusForbidden {
		t.Fatalf("remote desktop session was allowed: HTTP %d", w.Code)
	}
}
