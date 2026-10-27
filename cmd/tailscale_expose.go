package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"fotoro/internal/tailscale"
)

// ensureTailscaleExpose configures serve/funnel after the HTTP API is listening.
func ensureTailscaleExpose(s *Server, addr string) {
	ts := s.tailscale
	if ts == nil || os.Getenv("FOTORO_SKIP_SERVE") == "1" {
		return
	}
	if !ts.IsRunning() {
		return
	}

	baseURL := "http://" + addr
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if reachable(baseURL + "/api/health") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !reachable(baseURL + "/api/health") {
		fmt.Printf("[WARN] API not ready on %s — skipping tailscale serve\n", addr)
		return
	}

	port := fotoroServePort()
	if os.Getenv("FOTORO_PRIVATE_SITE") == "1" {
		port = 3000
		if raw := strings.TrimSpace(os.Getenv("FOTORO_SITE_PORT")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed < 65536 {
				port = parsed
			}
		}
		siteHealth := fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", port)
		deadline := time.Now().Add(time.Minute)
		for !reachable(siteHealth) && time.Now().Before(deadline) {
			time.Sleep(time.Second)
		}
		if !reachable(siteHealth) {
			fmt.Printf("[WARN] Private website is not ready at %s — skipping tailscale serve\n", siteHealth)
			return
		}
		fmt.Printf("[TAILSCALE] Exposing website and API on your tailnet (port %d)…\n", port)
	} else {
		fmt.Printf("[TAILSCALE] Exposing API on your tailnet (port %d)…\n", port)
	}

	publicURL := ""
	tailnetURL := ""
	serveEnabled := false
	funnelEnabled := false
	if ts.ServesPort(port) {
		serveEnabled = true
		fmt.Println("[TAILSCALE] Reusing existing Fotoro Serve route")
	} else if err := ts.SetupServe(port); err != nil {
		fmt.Printf("[WARN] tailscale serve failed: %v\n", err)
		fmt.Printf("[HINT] Grant this user Tailscale operator access, then run: tailscale serve --bg %d\n", port)
	} else {
		serveEnabled = true
	}
	if serveEnabled {
		url, err := ts.GetTailnetURL()
		if err == nil {
			tailnetURL = tailscale.NormalizeServerURL(url)
			fmt.Printf("[TAILSCALE] Tailnet URL: %s\n", url)
		}
	}

	if ts.ServesPort(port) && ts.FunnelConfigured() {
		funnelEnabled = true
	} else if os.Getenv("FOTORO_FUNNEL") == "1" {
		fmt.Printf("[TAILSCALE] Enabling public Funnel on port %d…\n", port)
		if err := ts.SetupFunnel(port); err != nil {
			fmt.Printf("[WARN] tailscale funnel failed: %v\n", err)
			fmt.Printf("[HINT] Enable Funnel in Tailscale admin ACL and grant operator access, then: tailscale funnel --bg %d\n", port)
		} else if url, err := ts.GetPublicURL(); err == nil {
			funnelEnabled = true
			publicURL = url
			fmt.Printf("[TAILSCALE] Public URL: %s\n", url)
		}
	}
	if funnelEnabled && publicURL == "" {
		publicURL, _ = ts.GetPublicURL()
	}

	if s.db != nil && (publicURL != "" || tailnetURL != "" || serveEnabled || funnelEnabled) {
		updates := map[string]interface{}{
			"serve_enabled":  boolToInt(serveEnabled),
			"funnel_enabled": boolToInt(funnelEnabled),
			"public_url":     "",
		}
		if publicURL != "" {
			updates["public_url"] = tailscale.NormalizeServerURL(publicURL)
		}
		if tailnetURL != "" {
			updates["tailnet_serve_url"] = tailnetURL
		}
		_ = s.db.UpdateServerConfig(updates)
		go s.syncNodeToCloud()
	}
}

func parseServerAddr(addr string) string {
	if strings.TrimSpace(addr) == "" {
		return "127.0.0.1:8765"
	}
	return addr
}
