package cliauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Session is a pending or completed CLI sign-in handoff row.
type Session struct {
	State        string
	AccessToken  string
	RefreshToken string
	UserID       string
	Email        string
	Name         string
	AvatarURL    string
}

type client struct {
	url        string
	anonKey    string
	serviceKey string
	http       *http.Client
}

func newClient() (*client, error) {
	baseURL := strings.TrimSpace(os.Getenv("SUPABASE_URL"))
	anonKey := strings.TrimSpace(os.Getenv("SUPABASE_ANON_KEY"))
	if baseURL == "" || anonKey == "" {
		return nil, fmt.Errorf("SUPABASE_URL and SUPABASE_ANON_KEY required")
	}
	return &client{
		url:        strings.TrimSuffix(baseURL, "/"),
		anonKey:    anonKey,
		serviceKey: strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_KEY")),
		http:       &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func IsConfigured() bool {
	return strings.TrimSpace(os.Getenv("SUPABASE_URL")) != "" &&
		strings.TrimSpace(os.Getenv("SUPABASE_ANON_KEY")) != ""
}

func HasServiceKey() bool {
	return strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_KEY")) != ""
}

// CreateSession registers a pending sign-in the website completes after auth.
func CreateSession(state string, ttl time.Duration) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	if c.serviceKey == "" {
		return fmt.Errorf("SUPABASE_SERVICE_KEY required for CLI sign-in handoff")
	}

	payload := map[string]interface{}{
		"state":      state,
		"expires_at": time.Now().Add(ttl).UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", c.url+"/rest/v1/cli_auth_sessions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.anonKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "resolution=ignore-duplicates")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 200 && resp.StatusCode != 409 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create cli session: %d %s", resp.StatusCode, string(b))
	}
	return nil
}

// TryFetchSession returns a completed handoff row if the website wrote tokens.
func TryFetchSession(state string) (*Session, bool, error) {
	c, err := newClient()
	if err != nil {
		return nil, false, err
	}
	sess, ok, err := fetchSession(c, state)
	if ok {
		_ = deleteSession(c, state)
	}
	return sess, ok, err
}

func fetchSession(c *client, state string) (*Session, bool, error) {
	key := c.serviceKey
	if key == "" {
		key = c.anonKey
	}
	u := fmt.Sprintf("%s/rest/v1/cli_auth_sessions?state=eq.%s&select=*&limit=1",
		c.url, url.QueryEscape(state))
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("apikey", c.anonKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false, nil
	}

	var rows []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil || len(rows) == 0 {
		return nil, false, nil
	}
	row := rows[0]
	access, _ := row["access_token"].(string)
	if strings.TrimSpace(access) == "" {
		return nil, false, nil
	}

	return &Session{
		State:        state,
		AccessToken:  access,
		RefreshToken: strField(row, "refresh_token"),
		UserID:       strField(row, "user_id"),
		Email:        strField(row, "email"),
		Name:         strField(row, "name"),
		AvatarURL:    strField(row, "avatar_url"),
	}, true, nil
}

func deleteSession(c *client, state string) error {
	if c.serviceKey == "" {
		return nil
	}
	req, err := http.NewRequest("DELETE",
		fmt.Sprintf("%s/rest/v1/cli_auth_sessions?state=eq.%s", c.url, url.QueryEscape(state)), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.anonKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func strField(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
