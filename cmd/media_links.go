package cmd

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/media"
)

const mediaLinkLifetime = 2 * time.Hour

// The key survives API sleep/wake cycles so links already in an open gallery
// remain valid. It never leaves the local database.
func loadMediaSigningKey(database *db.DB) ([]byte, error) {
	var encoded string
	if err := database.QueryRow(`SELECT COALESCE(media_signing_key, '') FROM server_config WHERE id = 1`).Scan(&encoded); err != nil {
		return nil, err
	}
	if encoded == "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if _, err := database.Exec(`UPDATE server_config SET media_signing_key = ?
			WHERE id = 1 AND COALESCE(media_signing_key, '') = ''`, hex.EncodeToString(key)); err != nil {
			return nil, err
		}
		if err := database.QueryRow(`SELECT media_signing_key FROM server_config WHERE id = 1`).Scan(&encoded); err != nil {
			return nil, err
		}
	}
	key, err := hex.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid media signing key")
	}
	return key, nil
}

func mediaSignature(key []byte, id, variant string, expires int64) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "fotoro-media-v1\n%s\n%s\n%d", id, variant, expires)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validMediaID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) publicMediaBase() string {
	if len(s.mediaSigningKey) == 0 || s.db == nil {
		return ""
	}
	var enabled int
	var raw string
	if err := s.db.QueryRow(`SELECT COALESCE(funnel_enabled, 0), COALESCE(public_url, '')
		FROM server_config WHERE id = 1`).Scan(&enabled, &raw); err != nil || enabled != 1 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".ts.net") ||
		u.User != nil || u.Port() != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (s *Server) signedMediaURL(base, id, variant string, expires int64) string {
	path := "/api/v1/media-file/" + id + "/" + variant
	query := "?expires=" + strconv.FormatInt(expires, 10) + "&signature=" + mediaSignature(s.mediaSigningKey, id, variant, expires)
	return base + path + query
}

func (s *Server) attachDirectMediaLinks(assets []media.Asset) {
	base := s.publicMediaBase()
	if base == "" {
		return
	}
	expires := time.Now().Truncate(15 * time.Minute).Add(mediaLinkLifetime).Unix()
	for i := range assets {
		a := &assets[i]
		if !validMediaID(a.ID) {
			continue
		}
		a.DirectContentURL = s.signedMediaURL(base, a.ID, "original", expires)
		if a.PreviewStatus == "ready" {
			a.DirectThumbnailURL = s.signedMediaURL(base, a.ID, "small", expires)
			a.DirectPreviewURL = s.signedMediaURL(base, a.ID, "medium", expires)
		}
	}
}

func (s *Server) handleSignedMedia(w http.ResponseWriter, r *http.Request) {
	if len(s.mediaSigningKey) != 32 {
		http.Error(w, "Media links unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET or HEAD only", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/media-file/"), "/")
	if len(parts) != 2 || !validMediaID(parts[0]) ||
		(parts[1] != "small" && parts[1] != "medium" && parts[1] != "original") {
		http.NotFound(w, r)
		return
	}
	expires, err := strconv.ParseInt(r.URL.Query().Get("expires"), 10, 64)
	now := time.Now().Unix()
	if err != nil || expires < now || expires > now+int64(mediaLinkLifetime.Seconds())+60 {
		http.Error(w, "Media link expired", http.StatusForbidden)
		return
	}
	provided, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("signature"))
	expected, _ := base64.RawURLEncoding.DecodeString(mediaSignature(s.mediaSigningKey, parts[0], parts[1], expires))
	if err != nil || !hmac.Equal(provided, expected) {
		http.Error(w, "Invalid media link", http.StatusForbidden)
		return
	}
	var path, mimeType string
	if parts[1] == "original" {
		path, mimeType, err = s.media.ContentPath(parts[0])
	} else {
		path, err = s.media.ThumbnailPath(parts[0], parts[1])
		mimeType = "image/jpeg"
	}
	if err != nil {
		writeMediaError(w, err)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, path)
}
