package localtoken

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const defaultTTL = 5 * time.Minute

// Claims are short-lived tokens for browser → funnel API access.
type Claims struct {
	UserID string `json:"sub"`
	jwt.RegisteredClaims
}

func secret() ([]byte, error) {
	if s := strings.TrimSpace(os.Getenv("FOTORO_LOCAL_TOKEN_SECRET")); s != "" {
		return []byte(s), nil
	}
	if s := strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_KEY")); s != "" {
		if len(s) >= 32 {
			return []byte(s[:32]), nil
		}
		return []byte(s), nil
	}
	return nil, fmt.Errorf("FOTORO_LOCAL_TOKEN_SECRET or SUPABASE_SERVICE_KEY required for local tokens")
}

// Verify parses and validates a 5-minute HS256 local access token.
func Verify(token string) (*Claims, error) {
	key, err := secret()
	if err != nil {
		return nil, err
	}
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid || claims.UserID == "" {
		return nil, fmt.Errorf("invalid local token")
	}
	return claims, nil
}

// TTL returns configured token lifetime.
func TTL() time.Duration {
	if mins := strings.TrimSpace(os.Getenv("FOTORO_LOCAL_TOKEN_MINUTES")); mins != "" {
		if m, err := time.ParseDuration(mins + "m"); err == nil && m > 0 {
			return m
		}
	}
	return defaultTTL
}
