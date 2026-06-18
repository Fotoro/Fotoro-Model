package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// TokenExpiry returns the exp claim time, or zero if unreadable.
func TokenExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid token format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim")
	}
	return time.Unix(int64(claims.Exp), 0), nil
}

// TokenNeedsRefresh reports whether the token is expired or expires within margin.
func TokenNeedsRefresh(token string, margin time.Duration) bool {
	exp, err := TokenExpiry(token)
	if err != nil {
		return true
	}
	return time.Until(exp) < margin
}

// RefreshSession exchanges a Supabase refresh token for a new access token.
func RefreshSession(refreshToken string) (accessToken, newRefresh string, err error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return "", "", fmt.Errorf("missing refresh token — run ./fotoro login")
	}

	baseURL := strings.TrimSuffix(strings.TrimSpace(os.Getenv("SUPABASE_URL")), "/")
	anonKey := strings.TrimSpace(os.Getenv("SUPABASE_ANON_KEY"))
	if baseURL == "" || anonKey == "" {
		return "", "", fmt.Errorf("SUPABASE_URL and SUPABASE_ANON_KEY required")
	}

	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	req, err := http.NewRequest("POST", baseURL+"/auth/v1/token?grant_type=refresh_token", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("apikey", anonKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", "", err
	}
	if out.AccessToken == "" {
		return "", "", fmt.Errorf("refresh returned empty access token")
	}
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	return out.AccessToken, out.RefreshToken, nil
}
