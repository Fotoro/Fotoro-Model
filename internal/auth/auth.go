package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ── User ────────────────────────────────────────────────────────────────────

type User struct {
	ID       string `json:"sub"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Avatar   string `json:"avatar_url"`
	Provider string `json:"provider"`
}

// ── Context ─────────────────────────────────────────────────────────────────

type contextKey string

const userContextKey contextKey = "fotoro_user"

func WithUser(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

func UserFromContext(ctx context.Context) *User {
	user, _ := ctx.Value(userContextKey).(*User)
	return user
}

func UserFromRequest(r *http.Request) *User {
	return UserFromContext(r.Context())
}

// ── SupabaseAuth ────────────────────────────────────────────────────────────

type SupabaseAuth struct {
	supabaseURL string
	anonKey     string
	jwks        *JWKS
	client      *http.Client
}

func NewSupabaseAuth(supabaseURL, anonKey string) *SupabaseAuth {
	return &SupabaseAuth{
		supabaseURL: strings.TrimSuffix(supabaseURL, "/"),
		anonKey:     anonKey,
		client:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (sa *SupabaseAuth) Initialize() error {
	jwksURL := fmt.Sprintf("%s/auth/v1/.well-known/jwks.json", sa.supabaseURL)
	resp, err := sa.client.Get(jwksURL)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS endpoint returned %d", resp.StatusCode)
	}

	var jwks JWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}

	sa.jwks = &jwks
	return nil
}

func (sa *SupabaseAuth) VerifyToken(tokenString string) (*User, error) {
	if sa.jwks == nil {
		if err := sa.Initialize(); err != nil {
			return nil, err
		}
	}

	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	kid, ok := token.Header["kid"].(string)
	if !ok {
		return nil, fmt.Errorf("no kid in token header")
	}

	key, err := sa.jwks.GetKey(kid)
	if err != nil {
		return nil, fmt.Errorf("get key: %w", err)
	}

	claims := jwt.MapClaims{}
	token, err = jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return key, nil
	}, jwt.WithIssuer(sa.supabaseURL+"/auth/v1"), jwt.WithValidMethods([]string{"RS256", "ES256"}), jwt.WithLeeway(120*time.Second))
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	if !token.Valid {
		return nil, fmt.Errorf("token invalid")
	}

	user := &User{ID: fmt.Sprintf("%v", claims["sub"])}
	if v, ok := claims["email"].(string); ok {
		user.Email = v
	}
	if meta, ok := claims["user_metadata"].(map[string]interface{}); ok {
		if v, ok := meta["full_name"].(string); ok {
			user.Name = v
		}
		if v, ok := meta["name"].(string); ok && user.Name == "" {
			user.Name = v
		}
		if v, ok := meta["avatar_url"].(string); ok {
			user.Avatar = v
		}
		if v, ok := meta["picture"].(string); ok && user.Avatar == "" {
			user.Avatar = v
		}
		if v, ok := meta["provider"].(string); ok {
			user.Provider = v
		}
	}

	return user, nil
}

func (sa *SupabaseAuth) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sa == nil {
			next(w, r)
			return
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, `{"error":"invalid authorization header"}`, http.StatusUnauthorized)
			return
		}
		user, err := sa.VerifyToken(parts[1])
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusUnauthorized)
			return
		}
		ctx := WithUser(r.Context(), user)
		next(w, r.WithContext(ctx))
	}
}

// ── JWKS ────────────────────────────────────────────────────────────────────

type JWKS struct {
	Keys []JWK `json:"keys"`
}

type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
	Crv string `json:"crv,omitempty"`
}

func (j *JWKS) GetKey(kid string) (interface{}, error) {
	for _, key := range j.Keys {
		if key.Kid == kid {
			return key.parse()
		}
	}
	return nil, fmt.Errorf("key %s not found", kid)
}

func (j *JWK) parse() (interface{}, error) {
	switch j.Kty {
	case "RSA":
		return j.parseRSA()
	case "EC":
		return j.parseEC()
	default:
		return nil, fmt.Errorf("unsupported key type: %s", j.Kty)
	}
}

func (j *JWK) parseRSA() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(j.N)
	if err != nil {
		return nil, fmt.Errorf("decode n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(j.E)
	if err != nil {
		return nil, fmt.Errorf("decode e: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	var e int
	if len(eBytes) < 4 {
		pad := make([]byte, 4-len(eBytes))
		eBytes = append(pad, eBytes...)
	}
	e = int(binary.BigEndian.Uint32(eBytes))
	return &rsa.PublicKey{N: n, E: e}, nil
}

func (j *JWK) parseEC() (*ecdsa.PublicKey, error) {
	xBytes, err := base64.RawURLEncoding.DecodeString(j.X)
	if err != nil {
		return nil, fmt.Errorf("decode x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(j.Y)
	if err != nil {
		return nil, fmt.Errorf("decode y: %w", err)
	}
	var curve elliptic.Curve
	switch j.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported curve: %s", j.Crv)
	}
	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}
