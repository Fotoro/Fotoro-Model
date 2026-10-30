package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"fotoro/internal/auth"
	"fotoro/internal/db"
)

func TestPairingRedeemDoesNotBlockSingleConnection(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "pairing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`UPDATE server_config SET auth_user_id = 'owner-1' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: database}
	createBody := bytes.NewBufferString(`{"server_url":"https://fotoro-server.example.ts.net"}`)
	create := httptest.NewRequest(http.MethodPost, "/api/v1/pairing/sessions", createBody)
	create = create.WithContext(auth.WithUser(create.Context(), &auth.User{ID: "owner-1"}))
	created := httptest.NewRecorder()
	s.handleCreatePairingSession(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create session: HTTP %d: %s", created.Code, created.Body.String())
	}
	var session struct {
		PairingToken string `json:"pairing_token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil || session.PairingToken == "" {
		t.Fatalf("decode session: %v", err)
	}

	redeemBody, _ := json.Marshal(map[string]string{
		"pairing_token": session.PairingToken,
		"device_id":     "phone-1",
		"device_name":   "Android phone",
		"platform":      "android",
	})
	redeem := httptest.NewRequest(http.MethodPost, "/api/v1/pairing/redeem", bytes.NewReader(redeemBody))
	redeemed := httptest.NewRecorder()
	s.handleRedeemPairingSession(redeemed, redeem)
	if redeemed.Code != http.StatusCreated {
		t.Fatalf("redeem session: HTTP %d: %s", redeemed.Code, redeemed.Body.String())
	}
	var device struct {
		DeviceToken string `json:"device_token"`
	}
	if err := json.Unmarshal(redeemed.Body.Bytes(), &device); err != nil || device.DeviceToken == "" {
		t.Fatalf("decode credential: %v", err)
	}
	user, err := s.verifyDeviceToken(device.DeviceToken)
	if err != nil || user.ID != "device:phone-1" {
		t.Fatalf("verify device credential: %v", err)
	}
	if err := s.authorizeUser(httptest.NewRequest(http.MethodGet, "/api/v1/media", nil), user); err != nil {
		t.Fatalf("use paired device: %v", err)
	}
	replayed := httptest.NewRecorder()
	s.handleRedeemPairingSession(replayed, httptest.NewRequest(http.MethodPost, "/api/v1/pairing/redeem", bytes.NewReader(redeemBody)))
	if replayed.Code != http.StatusUnauthorized {
		t.Fatalf("replayed session: HTTP %d", replayed.Code)
	}
}
