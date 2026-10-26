package cmd

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"fotoro/internal/auth"
)

// authorizeUser binds browser sessions to the account that registered this
// server. Device credentials are deliberately limited to the media v1 API.
func (s *Server) authorizeUser(r *http.Request, user *auth.User) error {
	if user == nil || user.ID == "" {
		return fmt.Errorf("missing identity")
	}
	if deviceID := deviceIDFromUser(user); deviceID != "" {
		var scopes, ownerID string
		err := s.db.QueryRow(`SELECT scopes, COALESCE(owner_id, '') FROM device_credentials
			WHERE device_id = ? AND revoked_at IS NULL`, deviceID).Scan(&scopes, &ownerID)
		if err != nil || ownerID == "" {
			return fmt.Errorf("device is not linked to an owner")
		}
		if bound, err := s.boundOwnerID(); err != nil || bound != ownerID {
			return fmt.Errorf("device owner mismatch")
		}
		need := "media:read"
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			need = "media:write"
		}
		path := r.URL.Path
		if !(path == "/api/v1/media" || strings.HasPrefix(path, "/api/v1/media/") ||
			path == "/api/v1/uploads" || strings.HasPrefix(path, "/api/v1/uploads/") ||
			path == "/api/v1/me") {
			return fmt.Errorf("device cannot access this endpoint")
		}
		if !hasScope(scopes, need) {
			return fmt.Errorf("device lacks %s scope", need)
		}
		return nil
	}
	ownerID, err := s.boundOwnerID()
	if err != nil || ownerID == "" || ownerID != user.ID {
		return fmt.Errorf("account is not linked to this server")
	}
	return nil
}

func (s *Server) boundOwnerID() (string, error) {
	var ownerID string
	err := s.db.QueryRow(`SELECT COALESCE(auth_user_id, '') FROM server_config WHERE id = 1`).Scan(&ownerID)
	return ownerID, err
}

func hasScope(scopes, required string) bool {
	for _, scope := range strings.Split(scopes, ",") {
		if strings.TrimSpace(scope) == required {
			return true
		}
	}
	return false
}

func localAuthBypassEnabled() bool {
	return os.Getenv("FOTORO_ALLOW_UNAUTHENTICATED_LOCAL") == "1"
}
