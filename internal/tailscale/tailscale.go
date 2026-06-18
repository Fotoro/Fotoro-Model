package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Config holds Tailscale configuration
type Config struct {
	TailnetName   string
	NodeName      string
	TailscaleIP   string
	AuthKey       string
	APIKey        string
	FunnelEnabled bool
	ServeEnabled  bool
}

// NodeInfo represents a Tailscale node
type NodeInfo struct {
	Name      string `json:"name"`
	Addresses []string `json:"addresses"`
	Online    bool   `json:"online"`
	Tags      []string `json:"tags"`
}

// Manager handles Tailscale operations
type Manager struct {
	config Config
}

func NewManager() *Manager {
	return &Manager{
		config: Config{
			NodeName: getEnv("FOTORO_NODE_NAME", "fotoro-server"),
		},
	}
}

// SetNodeName sets the MagicDNS hostname segment for this machine.
func (m *Manager) SetNodeName(name string) {
	if name != "" {
		m.config.NodeName = name
	}
}

// IsInstalled checks if tailscale CLI is available
func (m *Manager) IsInstalled() bool {
	_, err := exec.LookPath("tailscale")
	return err == nil
}

// IsRunning checks if tailscaled is active
func (m *Manager) IsRunning() bool {
	cmd := exec.Command("tailscale", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	var status struct {
		BackendState string `json:"BackendState"`
	}
	return json.Unmarshal(out, &status) == nil && status.BackendState == "Running"
}

// GetStatus returns current tailscale status
func (m *Manager) GetStatus() (map[string]interface{}, error) {
	cmd := exec.Command("tailscale", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tailscale status failed: %w", err)
	}
	var status map[string]interface{}
	if err := json.Unmarshal(out, &status); err != nil {
		return nil, err
	}
	return status, nil
}

// GetTailscaleIP returns the 100.x.y.z IP
func (m *Manager) GetTailscaleIP() (string, error) {
	status, err := m.GetStatus()
	if err != nil {
		return "", err
	}
	
	// Extract Self node addresses
	self, ok := status["Self"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("no Self node in status")
	}
	
	addrs, ok := self["TailscaleIPs"].([]interface{})
	if !ok || len(addrs) == 0 {
		return "", fmt.Errorf("no tailscale IPs found")
	}
	
	ip := addrs[0].(string)
	m.config.TailscaleIP = ip
	return ip, nil
}

// GetTailnetName extracts tailnet name from status
func (m *Manager) GetTailnetName() (string, error) {
	status, err := m.GetStatus()
	if err != nil {
		return "", err
	}
	
	currentTailnet, ok := status["CurrentTailnet"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("no CurrentTailnet in status")
	}
	
	name, ok := currentTailnet["Name"].(string)
	if !ok {
		return "", fmt.Errorf("no tailnet name found")
	}
	
	m.config.TailnetName = name
	return name, nil
}

// GetLoginName returns the Tailscale account email/name for this node.
func (m *Manager) GetLoginName() (string, error) {
	status, err := m.GetStatus()
	if err != nil {
		return "", err
	}
	users, ok := status["User"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("no user in status")
	}
	for _, v := range users {
		userMap, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if name, ok := userMap["LoginName"].(string); ok && name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("no login name found")
}

func (m *Manager) GenerateAuthKey(apiKey string, reusable bool, ephemeral bool, tags []string) (string, error) {
	if apiKey == "" {
		apiKey = os.Getenv("TAILSCALE_API_KEY")
	}
	if apiKey == "" {
		return "", fmt.Errorf("TAILSCALE_API_KEY not set")
	}
	
	url := "https://api.tailscale.com/api/v2/tailnet/-/keys"
	
	payload := map[string]interface{}{
		"capabilities": map[string]interface{}{
			"devices": map[string]interface{}{
				"create": map[string]interface{}{
					"reusable":   reusable,
					"ephemeral":  ephemeral,
					"preauthorized": true,
					"tags":       tags,
				},
			},
		},
		"expirySeconds": 86400 * 90, // 90 days
	}
	
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", url, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}
	
	var result struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	
	m.config.AuthKey = result.Key
	return result.Key, nil
}

// Logout removes this machine from the tailnet and clears local login state.
func (m *Manager) Logout() error {
	if !m.IsInstalled() {
		return nil
	}
	cmd := exec.Command("sudo", "tailscale", "logout")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	return m.Down()
}

// UpInteractive connects via Tailscale login URL (browser). No auth key required.
// Pass forceReauth=true after a reset so the user must sign in again.
func (m *Manager) UpInteractive(forceReauth bool) error {
	args := []string{"up", "--accept-routes", "--reset", "--hostname=" + m.config.NodeName}
	if forceReauth {
		args = append(args, "--force-reauth")
	}
	cmd := exec.Command("sudo", append([]string{"tailscale"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("[TAILSCALE] Sign in or create a free Tailscale account in your browser.")
	fmt.Println("[TAILSCALE] (Google / Microsoft / GitHub / email — personal plan is free)")
	fmt.Println("[TAILSCALE] Complete sign-in in the browser, then return here.")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tailscale up failed: %w", err)
	}

	for i := 0; i < 120; i++ {
		if m.IsRunning() {
			ip, _ := m.GetTailscaleIP()
			fmt.Printf("[TAILSCALE] Connected! IP: %s\n", ip)
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("tailscale connection timeout after login")
}

// Up connects to tailnet with auth key
func (m *Manager) Up(authKey string, tags []string) error {
	args := []string{"up", "--auth-key=" + authKey, "--accept-routes", "--reset", "--hostname=" + m.config.NodeName}
	if len(tags) > 0 {
		args = append(args, "--advertise-tags="+strings.Join(tags, ","))
	}
	
	cmd := exec.Command("sudo", append([]string{"tailscale"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	fmt.Println("[TAILSCALE] Connecting to tailnet...")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tailscale up failed: %w", err)
	}
	
	// Wait for connection
	for i := 0; i < 30; i++ {
		if m.IsRunning() {
			ip, _ := m.GetTailscaleIP()
			fmt.Printf("[TAILSCALE] Connected! IP: %s\n", ip)
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("tailscale connection timeout")
}

// SetupFunnel exposes the server publicly via HTTPS
func (m *Manager) SetupFunnel(localPort int) error {
	if !m.IsRunning() {
		return fmt.Errorf("tailscale not running")
	}

	fmt.Printf("[TAILSCALE] Setting up Funnel on port %d...\n", localPort)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sudo", "tailscale", "funnel", "--bg", fmt.Sprintf("%d", localPort))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			if m.ServeConfigured() {
				m.config.FunnelEnabled = true
				return nil
			}
			return fmt.Errorf("funnel setup timed out")
		}
		return fmt.Errorf("funnel setup failed: %w", err)
	}

	m.config.FunnelEnabled = true
	fmt.Println("[TAILSCALE] Funnel active — accessible from internet")
	return nil
}

// SetupServe exposes server within tailnet only
func (m *Manager) SetupServe(localPort int) error {
	if !m.IsRunning() {
		return fmt.Errorf("tailscale not running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sudo", "tailscale", "serve", "--bg", fmt.Sprintf("%d", localPort))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			if m.ServeConfigured() {
				m.config.ServeEnabled = true
				fmt.Println("[TAILSCALE] Serve active — accessible within tailnet")
				return nil
			}
			return fmt.Errorf("serve setup timed out (is the API running on port %d?)", localPort)
		}
		return fmt.Errorf("serve setup failed: %w", err)
	}

	m.config.ServeEnabled = true
	fmt.Println("[TAILSCALE] Serve active — accessible within tailnet")
	return nil
}

// ServeConfigured reports whether tailscale serve has an active config.
func (m *Manager) ServeConfigured() bool {
	cmd := exec.Command("tailscale", "serve", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	var status map[string]interface{}
	if json.Unmarshal(out, &status) != nil {
		return false
	}
	// Empty config is {} or has no TCP/HTTP handlers
	if len(status) == 0 {
		return false
	}
	for k, v := range status {
		if k == "TCP" || k == "Web" || k == "AllowFunnel" {
			if m, ok := v.(map[string]interface{}); ok && len(m) > 0 {
				return true
			}
		}
	}
	return len(status) > 0
}

// GetMagicDNS returns the full MagicDNS hostname (e.g. fotoro-server.tail650297.ts.net).
// Uses Tailscale's Self.DNSName — never builds from the account email, because
// emails contain "@" which breaks URLs in browsers.
func (m *Manager) GetMagicDNS() (string, error) {
	status, err := m.GetStatus()
	if err != nil {
		return "", err
	}

	if self, ok := status["Self"].(map[string]interface{}); ok {
		if dns, ok := self["DNSName"].(string); ok && dns != "" {
			return strings.TrimSuffix(dns, "."), nil
		}
	}

	if ct, ok := status["CurrentTailnet"].(map[string]interface{}); ok {
		if suffix, ok := ct["MagicDNSSuffix"].(string); ok && suffix != "" {
			suffix = strings.TrimSuffix(suffix, ".")
			host := m.config.NodeName
			if host == "" {
				host = "fotoro-server"
			}
			return host + "." + suffix, nil
		}
	}

	return "", fmt.Errorf("no magic DNS name found")
}

// GetPublicURL returns the funnel URL if enabled
func (m *Manager) GetPublicURL() (string, error) {
	if !m.config.FunnelEnabled {
		return "", fmt.Errorf("funnel not enabled")
	}
	magicDNS, err := m.GetMagicDNS()
	if err != nil {
		return "", err
	}
	return PublicURL(magicDNS), nil
}

// GetTailnetURL returns the serve URL
func (m *Manager) GetTailnetURL() (string, error) {
	if !m.config.ServeEnabled && !m.config.FunnelEnabled {
		return "", fmt.Errorf("neither serve nor funnel enabled")
	}
	magicDNS, err := m.GetMagicDNS()
	if err != nil {
		return "", err
	}
	return PublicURL(magicDNS), nil
}

// RegisterWithOnlineDB sends the tailscale IP to your online service
func (m *Manager) RegisterWithOnlineDB(apiEndpoint string, userToken string) error {
	ip, err := m.GetTailscaleIP()
	if err != nil {
		return err
	}
	
	tailnet, err := m.GetTailnetName()
	if err != nil {
		return err
	}
	
	magicDNS, _ := m.GetMagicDNS()
	
	payload := map[string]interface{}{
		"tailscale_ip":   ip,
		"tailnet_name":   tailnet,
		"magic_dns":      magicDNS,
		"node_name":      m.config.NodeName,
		"funnel_enabled": m.config.FunnelEnabled,
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
	}
	
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", apiEndpoint+"/api/nodes/register", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("online DB registration failed: %d %s", resp.StatusCode, string(respBody))
	}
	
	fmt.Printf("[TAILSCALE] Registered with online DB: %s\\n", ip)
	return nil
}

// VerifyConnection tests if the node is reachable
func (m *Manager) VerifyConnection() error {
	ip, err := m.GetTailscaleIP()
	if err != nil {
		return err
	}
	
	// Try to ping our own IP
	cmd := exec.Command("tailscale", "ping", ip)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}
	
	if strings.Contains(string(out), "pong") || strings.Contains(string(out), "is local") {
		return nil
	}
	return fmt.Errorf("tailscale ping failed: %s", string(out))
}

// GetPeers returns list of connected peers
func (m *Manager) GetPeers() ([]NodeInfo, error) {
	status, err := m.GetStatus()
	if err != nil {
		return nil, err
	}
	
	peersRaw, ok := status["Peer"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("no peers in status")
	}
	
	var peers []NodeInfo
	for _, v := range peersRaw {
		peerMap, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		
		node := NodeInfo{}
		if name, ok := peerMap["HostName"].(string); ok {
			node.Name = name
		}
		if addrs, ok := peerMap["TailscaleIPs"].([]interface{}); ok {
			for _, a := range addrs {
				if s, ok := a.(string); ok {
					node.Addresses = append(node.Addresses, s)
				}
			}
		}
		if online, ok := peerMap["Online"].(bool); ok {
			node.Online = online
		}
		if tags, ok := peerMap["Tags"].([]interface{}); ok {
			for _, t := range tags {
				if s, ok := t.(string); ok {
					node.Tags = append(node.Tags, s)
				}
			}
		}
		peers = append(peers, node)
	}
	
	return peers, nil
}

// Down disconnects from tailnet
func (m *Manager) Down() error {
	cmd := exec.Command("sudo", "tailscale", "down")
	return cmd.Run()
}

// StatusString returns human-readable status
func (m *Manager) StatusString() string {
	if !m.IsInstalled() {
		return "Tailscale: NOT INSTALLED"
	}
	if !m.IsRunning() {
		return "Tailscale: installed but NOT CONNECTED"
	}
	
	ip, _ := m.GetTailscaleIP()
	tailnet, _ := m.GetTailnetName()
	magicDNS, _ := m.GetMagicDNS()
	
	status := fmt.Sprintf("Tailscale: CONNECTED\\n")
	status += fmt.Sprintf("  IP:        %s\\n", ip)
	status += fmt.Sprintf("  Tailnet:   %s\\n", tailnet)
	status += fmt.Sprintf("  MagicDNS:  %s\\n", magicDNS)
	status += fmt.Sprintf("  Funnel:    %v\\n", m.config.FunnelEnabled)
	status += fmt.Sprintf("  Serve:     %v\\n", m.config.ServeEnabled)
	
	peers, _ := m.GetPeers()
	status += fmt.Sprintf("  Peers:     %d online\\n", len(peers))
	
	return status
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// ExtractTailscaleIPFromString extracts 100.x.x.x from any string
func ExtractTailscaleIPFromString(s string) string {
	re := regexp.MustCompile(`100\\.(?:[0-9]{1,3}\\.){2}[0-9]{1,3}`)
	if match := re.FindString(s); match != "" {
		return match
	}
	return ""
}
