package cmd

import (
	"fmt"
	"os"
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
	fmt.Printf("[TAILSCALE] Exposing API on your tailnet (port %d)…\n", port)

	publicURL := ""
	if err := ts.SetupServe(port); err != nil {
		fmt.Printf("[WARN] tailscale serve failed: %v\n", err)
		fmt.Printf("[HINT] Run manually: sudo tailscale serve --bg %d\n", port)
	} else if url, err := ts.GetTailnetURL(); err == nil {
		publicURL = url
		fmt.Printf("[TAILSCALE] Tailnet URL: %s\n", url)
	}

	if os.Getenv("FOTORO_FUNNEL") != "0" {
		fmt.Printf("[TAILSCALE] Enabling public Funnel on port %d…\n", port)
		if err := ts.SetupFunnel(port); err != nil {
			fmt.Printf("[WARN] tailscale funnel failed: %v\n", err)
			fmt.Printf("[HINT] Enable Funnel in Tailscale admin ACL, then: sudo tailscale funnel --bg %d\n", port)
		} else if url, err := ts.GetPublicURL(); err == nil {
			publicURL = url
			fmt.Printf("[TAILSCALE] Public URL: %s\n", url)
		}
	}

	if s.db != nil && publicURL != "" {
		publicURL = tailscale.NormalizeServerURL(publicURL)
		_ = s.db.UpdateServerConfig(map[string]interface{}{
			"public_url":        publicURL,
			"tailnet_serve_url": publicURL,
		})
		go s.syncNodeToCloud()
	}
}

func parseServerAddr(addr string) string {
	if strings.TrimSpace(addr) == "" {
		return "127.0.0.1:8765"
	}
	return addr
}
