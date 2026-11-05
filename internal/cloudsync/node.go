package cloudsync

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/tailscale"
)

// NodeInfo is sent to fotoro.vercel.app/api/nodes for the dashboard.
type NodeInfo struct {
	TailscaleIP string
	TailnetName string
	MagicDNS    string
	NodeName    string
	PublicURL   string
	TailnetURL  string
}

type nodeEndpointEnvelope struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type nodeEndpointDetails struct {
	TailscaleIP string `json:"tailscale_ip"`
	TailnetName string `json:"tailnet_name,omitempty"`
	MagicDNS    string `json:"magic_dns,omitempty"`
	PublicURL   string `json:"public_url,omitempty"`
	TailnetURL  string `json:"tailnet_url,omitempty"`
}

const nodeEndpointAAD = "fotoro-node-endpoint-v1"

// WebAPIBase returns the website origin (no trailing slash).
func WebAPIBase() string {
	if base := strings.TrimSpace(os.Getenv("FOTORO_WEB_URL")); base != "" {
		return strings.TrimSuffix(base, "/")
	}
	authURL := strings.TrimSpace(os.Getenv("FOTORO_AUTH_URL"))
	if authURL == "" {
		authURL = "https://fotoro.vercel.app/login"
	}
	authURL = strings.TrimSuffix(authURL, "/")
	authURL = strings.TrimSuffix(authURL, "/login")
	return authURL
}

// SyncNode registers or updates this machine in Supabase via the website API.
func SyncNode(database *db.DB, accessToken string, node NodeInfo) error {
	if database == nil {
		return fmt.Errorf("missing local database")
	}
	if accessToken == "" {
		return fmt.Errorf("missing access token")
	}
	if node.TailscaleIP == "" {
		return fmt.Errorf("missing tailscale IP")
	}
	if node.NodeName == "" {
		node.NodeName = "fotoro-server"
	}

	key, err := database.GetOrCreateEndpointKey()
	if err != nil {
		return err
	}
	endpoint, err := json.Marshal(nodeEndpointDetails{
		TailscaleIP: node.TailscaleIP,
		TailnetName: node.TailnetName,
		MagicDNS:    node.MagicDNS,
		PublicURL:   node.PublicURL,
		TailnetURL:  node.TailnetURL,
	})
	if err != nil {
		return fmt.Errorf("encode node endpoint: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("initialize endpoint encryption: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("initialize endpoint authentication: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate endpoint nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, endpoint, []byte(nodeEndpointAAD))
	envelope := nodeEndpointEnvelope{
		Version:    1,
		Algorithm:  "A256GCM",
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}

	payload := map[string]interface{}{
		"node_name":           node.NodeName,
		"status":              "online",
		"endpoint_ciphertext": envelope,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", WebAPIBase()+"/api/nodes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("node sync request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node sync failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// NodeInfoFromTailscale reads live Tailscale status.
func NodeInfoFromTailscale(ts *tailscale.Manager, nodeName string) (NodeInfo, error) {
	if nodeName == "" {
		nodeName = "fotoro-server"
	}
	ip, err := ts.GetTailscaleIP()
	if err != nil {
		return NodeInfo{}, err
	}
	tailnet, _ := ts.GetTailnetName()
	magicDNS, _ := ts.GetMagicDNS()
	return NodeInfo{
		TailscaleIP: ip,
		TailnetName: tailnet,
		MagicDNS:    magicDNS,
		NodeName:    nodeName,
	}, nil
}

// SyncNodeFromDB uses stored auth + server_config (after setup or tailscale connect).
func SyncNodeFromDB(database *db.DB) error {
	accessToken, err := FreshAccessToken(database)
	if err != nil {
		return err
	}

	cfg, err := database.GetServerConfig()
	if err != nil {
		return err
	}

	ip, _ := cfg["tailscale_ip"].(string)
	if ip == "" {
		ts := tailscale.NewManager()
		if ts.IsRunning() {
			node, err := NodeInfoFromTailscale(ts, stringField(cfg, "server_name"))
			if err != nil {
				return fmt.Errorf("tailscale connected but no IP in config: %w", err)
			}
			return SyncNode(database, accessToken, node)
		}
		return fmt.Errorf("tailscale not configured — run ./fotoro setup")
	}

	node := NodeInfo{
		TailscaleIP: ip,
		TailnetName: stringField(cfg, "tailnet_name"),
		MagicDNS:    stringField(cfg, "tailnet_url"),
		NodeName:    stringField(cfg, "server_name"),
		PublicURL:   tailscale.NormalizeServerURL(stringField(cfg, "public_url")),
		TailnetURL:  stringField(cfg, "tailnet_serve_url"),
	}
	if node.TailnetURL == "" && node.MagicDNS != "" {
		node.TailnetURL = tailscale.PublicURL(node.MagicDNS)
	}
	if node.NodeName == "" {
		node.NodeName = "fotoro-server"
	}
	return SyncNode(database, accessToken, node)
}

func stringField(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// SyncNodeLive captures current Tailscale info and syncs to the website.
func SyncNodeLive(database *db.DB, ts *tailscale.Manager) error {
	accessToken, err := FreshAccessToken(database)
	if err != nil {
		return err
	}
	cfg, _ := database.GetServerConfig()
	nodeName := "fotoro-server"
	if cfg != nil {
		if n := stringField(cfg, "server_name"); n != "" {
			nodeName = n
		}
	}
	node, err := NodeInfoFromTailscale(ts, nodeName)
	if err != nil {
		return err
	}
	if cfg != nil {
		if u := stringField(cfg, "tailnet_serve_url"); u != "" {
			node.TailnetURL = tailscale.NormalizeServerURL(u)
		}
	}
	if u := strings.TrimSpace(os.Getenv("FOTORO_PUBLIC_URL")); u != "" {
		node.PublicURL = tailscale.NormalizeServerURL(u)
	} else if url, err := ts.GetPublicURL(); err == nil {
		node.PublicURL = tailscale.NormalizeServerURL(url)
	} else if ts.FunnelConfigured() && node.MagicDNS != "" {
		node.PublicURL = tailscale.PublicURL(node.MagicDNS)
	} else if cfg != nil && os.Getenv("FOTORO_TRUST_STORED_PUBLIC_URL") == "1" {
		node.PublicURL = tailscale.NormalizeServerURL(stringField(cfg, "public_url"))
	}
	if node.TailnetURL == "" {
		if url, err := ts.GetTailnetURL(); err == nil {
			node.TailnetURL = tailscale.NormalizeServerURL(url)
		} else if node.MagicDNS != "" {
			node.TailnetURL = tailscale.PublicURL(node.MagicDNS)
		}
	}
	if err := SyncNode(database, accessToken, node); err != nil {
		return err
	}
	updates := map[string]interface{}{
		"tailscale_enabled": 1,
		"tailscale_ip":      node.TailscaleIP,
		"tailnet_name":      node.TailnetName,
		"tailnet_url":       node.MagicDNS,
		"server_name":       node.NodeName,
		"tailnet_serve_url": node.TailnetURL,
		"funnel_enabled":    boolToInt(node.PublicURL != ""),
		"serve_enabled":     boolToInt(node.TailnetURL != ""),
	}
	if node.PublicURL != "" {
		updates["public_url"] = node.PublicURL
	}
	_ = database.UpdateServerConfig(updates)
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
