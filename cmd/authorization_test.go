package cmd

import (
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
