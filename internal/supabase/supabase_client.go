package supabase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/supabase-community/supabase-go"
)

// Client wraps the Supabase Go client with Fotoro-specific helpers
type Client struct {
	client     *supabase.Client
	url        string
	anonKey    string
	serviceKey string
	httpClient *http.Client
}

// NewClient creates a Supabase client from env vars
func NewClient() (*Client, error) {
	url := os.Getenv("SUPABASE_URL")
	anonKey := os.Getenv("SUPABASE_ANON_KEY")
	serviceKey := os.Getenv("SUPABASE_SERVICE_KEY")
	
	if url == "" || anonKey == "" {
		return nil, fmt.Errorf("SUPABASE_URL and SUPABASE_ANON_KEY required")
	}
	
	client, err := supabase.NewClient(url, anonKey, "", nil)
	if err != nil {
		return nil, fmt.Errorf("supabase init: %w", err)
	}
	
	return &Client{
		client:     client,
		url:        url,
		anonKey:    anonKey,
		serviceKey: serviceKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// ── AUTH ────────────────────────────────────────────────────────────────────

// SignUp creates a new user account
func (c *Client) SignUp(email, password string) (map[string]interface{}, error) {
	data, err := c.client.SignUp(email, password)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// SignIn authenticates user
func (c *Client) SignIn(email, password string) (string, error) {
	data, err := c.client.SignInWithEmailPassword(email, password)
	if err != nil {
		return "", err
	}
	
	// Extract access token
	if session, ok := data["session"].(map[string]interface{}); ok {
		if token, ok := session["access_token"].(string); ok {
			return token, nil
		}
	}
	return "", fmt.Errorf("no access token in response")
}

// GetUser returns current user info
func (c *Client) GetUser(token string) (map[string]interface{}, error) {
	req, err := http.NewRequest("GET", c.url+"/auth/v1/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("apikey", c.anonKey)
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("get user failed: %d %s", resp.StatusCode, string(body))
	}
	
	var user map[string]interface{}
	json.Unmarshal(body, &user)
	return user, nil
}

// ── NODE REGISTRATION (Tailscale IP storage) ────────────────────────────────

// RegisterNode stores the server's Tailscale IP in Supabase
func (c *Client) RegisterNode(userID, tailscaleIP, tailnetName, magicDNS, nodeName string) error {
	if c.serviceKey == "" {
		return fmt.Errorf("SUPABASE_SERVICE_KEY required for admin operations")
	}
	
	payload := map[string]interface{}{
		"user_id":       userID,
		"tailscale_ip":  tailscaleIP,
		"tailnet_name":  tailnetName,
		"magic_dns":     magicDNS,
		"node_name":     nodeName,
		"status":        "online",
		"last_seen":     time.Now().UTC().Format(time.RFC3339),
	}
	
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", c.url+"/rest/v1/nodes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.anonKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "resolution=merge-duplicates")
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register node failed: %d %s", resp.StatusCode, string(body))
	}
	return nil
}

// GetNodeByUserID retrieves a user's node info
func (c *Client) GetNodeByUserID(userID string) (map[string]interface{}, error) {
	req, err := http.NewRequest("GET", 
		c.url+"/rest/v1/nodes?user_id=eq."+userID+"&select=*&limit=1", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.anonKey)
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("get node failed: %d", resp.StatusCode)
	}
	
	var nodes []map[string]interface{}
	json.Unmarshal(body, &nodes)
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no node found")
	}
	return nodes[0], nil
}

// UpdateNodeHeartbeat updates last_seen timestamp
func (c *Client) UpdateNodeHeartbeat(nodeID string) error {
	payload := map[string]interface{}{
		"last_seen": time.Now().UTC().Format(time.RFC3339),
		"status":    "online",
	}
	
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("PATCH", 
		c.url+"/rest/v1/nodes?id=eq."+nodeID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.anonKey)
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("heartbeat failed: %d", resp.StatusCode)
	}
	return nil
}

// ── REALTIME (for mobile app sync) ──────────────────────────────────────────

// SubscribeToChanges listens for DB changes via Supabase Realtime
func (c *Client) SubscribeToChanges(table string, callback func(map[string]interface{})) error {
	// This requires the realtime client which needs WebSocket support
	// For now, we poll as a fallback
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		
		lastCheck := time.Now()
		for range ticker.C {
			changes, err := c.getChangesSince(table, lastCheck)
			if err == nil {
				for _, change := range changes {
					callback(change)
				}
			}
			lastCheck = time.Now()
		}
	}()
	return nil
}

func (c *Client) getChangesSince(table string, since time.Time) ([]map[string]interface{}, error) {
	url := fmt.Sprintf("%s/rest/v1/%s?created_at=gte.%s&select=*", 
		c.url, table, since.Format(time.RFC3339))
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.anonKey)
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	body, _ := io.ReadAll(resp.Body)
	var changes []map[string]interface{}
	json.Unmarshal(body, &changes)
	return changes, nil
}

// ── STORAGE (for backup/restore) ────────────────────────────────────────────

// UploadBackup uploads the SQLite DB to Supabase Storage
func (c *Client) UploadBackup(localPath, bucket string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	
	filename := fmt.Sprintf("fotoro_backup_%s.db", time.Now().Format("20060102_150405"))
	
	// Use Storage API directly
	url := fmt.Sprintf("%s/storage/v1/object/%s/%s", c.url, bucket, filename)
	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceKey)
	req.Header.Set("apikey", c.anonKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed: %d %s", resp.StatusCode, string(body))
	}
	return nil
}

// ── HELPER ──────────────────────────────────────────────────────────────────

// IsConfigured returns true if Supabase env vars are set
func IsConfigured() bool {
	return os.Getenv("SUPABASE_URL") != "" && os.Getenv("SUPABASE_ANON_KEY") != ""
}
