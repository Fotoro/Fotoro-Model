package cloudsync

import (
	"fmt"
	"time"

	"fotoro/internal/auth"
	"fotoro/internal/db"
)

const tokenRefreshMargin = 2 * time.Minute

// FreshAccessToken returns a valid access token, refreshing via Supabase when needed.
func FreshAccessToken(database *db.DB) (string, error) {
	sess, err := database.GetAuthSession()
	if err != nil {
		return "", err
	}
	if sess == nil || sess.AccessToken == "" {
		return "", fmt.Errorf("not signed in — run ./fotoro login")
	}

	if !auth.TokenNeedsRefresh(sess.AccessToken, tokenRefreshMargin) {
		return sess.AccessToken, nil
	}
	if sess.RefreshToken == "" {
		return "", fmt.Errorf("session expired — run ./fotoro login")
	}

	access, refresh, err := auth.RefreshSession(sess.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("session expired — run ./fotoro login: %w", err)
	}

	_ = database.SaveAuthSession(db.AuthSession{
		AccessToken:  access,
		RefreshToken: refresh,
		UserID:       sess.UserID,
		Email:        sess.Email,
		Name:         sess.Name,
		AvatarURL:    sess.AvatarURL,
	})
	return access, nil
}
