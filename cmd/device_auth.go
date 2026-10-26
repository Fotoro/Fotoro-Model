package cmd

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fotoro/internal/auth"
)

const deviceTokenPrefix = "fdev_"

func (s *Server) verifyDeviceToken(token string) (*auth.User, error) {
	if s.db == nil || !strings.HasPrefix(token, deviceTokenPrefix) {
		return nil, fmt.Errorf("not a device token")
	}
	hash := sha256.Sum256([]byte(token))
	var deviceID, name, platform string
	err := s.db.QueryRow(`
		SELECT c.device_id, c.name, c.platform FROM device_credentials c
		JOIN mobile_devices m ON m.device_id = c.device_id
		WHERE c.token_hash = ? AND c.revoked_at IS NULL AND m.is_active = 1
	`, hex.EncodeToString(hash[:])).Scan(&deviceID, &name, &platform)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("invalid device token")
	}
	if err != nil {
		return nil, err
	}
	_, _ = s.db.Exec(`UPDATE device_credentials SET last_used_at = ? WHERE device_id = ?`, time.Now().UTC(), deviceID)
	return &auth.User{
		ID:       "device:" + deviceID,
		Name:     name,
		Provider: "fotoro-device:" + platform,
	}, nil
}

func deviceIDFromUser(user *auth.User) string {
	if user == nil || !strings.HasPrefix(user.ID, "device:") {
		return ""
	}
	return strings.TrimPrefix(user.ID, "device:")
}

func (s *Server) handleCreatePairingSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ServerURL string `json:"server_url"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&body)

	secret, err := secureToken(32)
	if err != nil {
		http.Error(w, `{"error":"could not create pairing session"}`, http.StatusInternalServerError)
		return
	}
	id, err := secureToken(16)
	if err != nil {
		http.Error(w, `{"error":"could not create pairing session"}`, http.StatusInternalServerError)
		return
	}
	hash := sha256.Sum256([]byte(secret))
	createdBy := "local"
	if user := auth.UserFromRequest(r); user != nil && user.ID != "" {
		createdBy = user.ID
	}
	now := time.Now().UTC()
	expires := now.Add(10 * time.Minute)
	_, err = s.db.Exec(`
		INSERT INTO pairing_sessions (id, secret_hash, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, hex.EncodeToString(hash[:]), createdBy, now, expires)
	if err != nil {
		http.Error(w, `{"error":"could not save pairing session"}`, http.StatusInternalServerError)
		return
	}

	serverURL := cleanPairingServerURL(body.ServerURL)
	if serverURL == "" {
		serverURL = requestServerURL(r)
	}
	qrPayload, _ := json.Marshal(map[string]interface{}{
		"version":       1,
		"server_url":    serverURL,
		"pairing_token": secret,
	})
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":            id,
		"pairing_token": secret,
		"expires_at":    expires.Format(time.RFC3339),
		"server_url":    serverURL,
		"qr_payload":    string(qrPayload),
	})
}

func (s *Server) handleRedeemPairingSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PairingToken string `json:"pairing_token"`
		DeviceID     string `json:"device_id"`
		DeviceName   string `json:"device_name"`
		Platform     string `json:"platform"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	if len(body.PairingToken) < 32 {
		http.Error(w, `{"error":"invalid pairing token"}`, http.StatusUnauthorized)
		return
	}
	if strings.TrimSpace(body.DeviceName) == "" {
		body.DeviceName = "Android phone"
	}
	if strings.TrimSpace(body.Platform) == "" {
		body.Platform = "android"
	}
	if body.DeviceID == "" {
		var err error
		body.DeviceID, err = secureToken(12)
		if err != nil {
			http.Error(w, `{"error":"could not create device"}`, http.StatusInternalServerError)
			return
		}
	}
	if len(body.DeviceID) > 128 || len(body.DeviceName) > 128 || len(body.Platform) > 32 {
		http.Error(w, `{"error":"device fields are too long"}`, http.StatusBadRequest)
		return
	}

	secretHash := sha256.Sum256([]byte(body.PairingToken))
	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, `{"error":"database unavailable"}`, http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	var pairingID, createdBy string
	var expires time.Time
	var usedAt sql.NullTime
	err = tx.QueryRow(`
		SELECT id, created_by, expires_at, used_at FROM pairing_sessions WHERE secret_hash = ?
	`, hex.EncodeToString(secretHash[:])).Scan(&pairingID, &createdBy, &expires, &usedAt)
	if errors.Is(err, sql.ErrNoRows) || usedAt.Valid || time.Now().After(expires) {
		http.Error(w, `{"error":"pairing token is invalid, expired, or already used"}`, http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"database unavailable"}`, http.StatusInternalServerError)
		return
	}
	ownerID, err := s.boundOwnerID()
	if err != nil || ownerID == "" || createdBy != ownerID {
		http.Error(w, `{"error":"pairing owner mismatch"}`, http.StatusForbidden)
		return
	}

	deviceTokenSecret, err := secureToken(32)
	if err != nil {
		http.Error(w, `{"error":"could not create device credential"}`, http.StatusInternalServerError)
		return
	}
	deviceToken := deviceTokenPrefix + deviceTokenSecret
	tokenHash := sha256.Sum256([]byte(deviceToken))
	now := time.Now().UTC()
	if _, err = tx.Exec(`
		INSERT INTO mobile_devices (device_name, device_id, platform, paired_at, last_seen, is_active)
		VALUES (?, ?, ?, ?, ?, 1)
		ON CONFLICT(device_id) DO UPDATE SET
			device_name = excluded.device_name,
			platform = excluded.platform,
			last_seen = excluded.last_seen,
			is_active = 1
	`, body.DeviceName, body.DeviceID, body.Platform, now, now); err != nil {
		http.Error(w, `{"error":"could not register device"}`, http.StatusInternalServerError)
		return
	}
	if _, err = tx.Exec(`
		INSERT INTO device_credentials (device_id, owner_id, token_hash, name, platform, scopes, created_at)
		VALUES (?, ?, ?, ?, ?, 'media:read,media:write', ?)
		ON CONFLICT(device_id) DO UPDATE SET
			owner_id = excluded.owner_id,
			token_hash = excluded.token_hash,
			name = excluded.name,
			platform = excluded.platform,
			scopes = excluded.scopes,
			created_at = excluded.created_at,
			last_used_at = NULL,
			revoked_at = NULL
	`, body.DeviceID, ownerID, hex.EncodeToString(tokenHash[:]), body.DeviceName, body.Platform, now); err != nil {
		http.Error(w, `{"error":"could not save device credential"}`, http.StatusInternalServerError)
		return
	}
	res, err := tx.Exec(`
		UPDATE pairing_sessions SET used_at = ?, device_id = ?
		WHERE id = ? AND used_at IS NULL
	`, now, body.DeviceID, pairingID)
	if err != nil {
		http.Error(w, `{"error":"could not finish pairing"}`, http.StatusInternalServerError)
		return
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		http.Error(w, `{"error":"pairing token was already used"}`, http.StatusConflict)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, `{"error":"could not finish pairing"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"device_id":    body.DeviceID,
		"device_name":  body.DeviceName,
		"platform":     body.Platform,
		"device_token": deviceToken,
		"token_type":   "Bearer",
		"api_base":     "/api/v1",
	})
}

func secureToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func cleanPairingServerURL(raw string) string {
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "/"))
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/")
}

func requestServerURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
