package cloudsync

import (
	"bytes"
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
func SyncNode(accessToken string, node NodeInfo) error {
	if accessToken == "" {
		return fmt.Errorf("missing access token")
	}
	if node.TailscaleIP == "" {
		return fmt.Errorf("missing tailscale IP")
	}
	if node.NodeName == "" {
		node.NodeName = "fotoro-server"
	}

	payload := map[string]interface{}{
		"tailscale_ip": node.TailscaleIP,
		"tailnet_name": node.TailnetName,
		"magic_dns":    node.MagicDNS,
		"node_name":    node.NodeName,
		"status":       "online",
	}
	if node.PublicURL != "" {
		payload["public_url"] = node.PublicURL
	}
	if node.TailnetURL != "" {
		payload["tailnet_url"] = node.TailnetURL
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
			return SyncNode(accessToken, node)
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
	if node.TailnetURL == "" {
		node.TailnetURL = node.PublicURL
	}
	if node.PublicURL == "" && node.MagicDNS != "" {
		node.PublicURL = tailscale.PublicURL(node.MagicDNS)
	}
	if node.NodeName == "" {
		node.NodeName = "fotoro-server"
	}
	return SyncNode(accessToken, node)
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
		if u := stringField(cfg, "public_url"); u != "" {
			node.PublicURL = u
		}
		if u := stringField(cfg, "tailnet_serve_url"); u != "" {
			node.TailnetURL = u
		}
	}
	if node.PublicURL == "" {
		if url, err := ts.GetPublicURL(); err == nil {
			node.PublicURL = url
		} else if url, err := ts.GetTailnetURL(); err == nil {
			node.PublicURL = url
		}
	}
	if node.PublicURL == "" && node.MagicDNS != "" {
		node.PublicURL = tailscale.PublicURL(node.MagicDNS)
	}
	if node.TailnetURL == "" {
		node.TailnetURL = node.PublicURL
	}
	if err := SyncNode(accessToken, node); err != nil {
		return err
	}
	_ = database.UpdateServerConfig(map[string]interface{}{
		"tailscale_enabled": 1,
		"tailscale_ip":      node.TailscaleIP,
		"tailnet_name":      node.TailnetName,
		"tailnet_url":       node.MagicDNS,
		"server_name":       node.NodeName,
	})
	return nil
}
