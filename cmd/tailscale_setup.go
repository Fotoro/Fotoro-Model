package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"fotoro/internal/db"
	"fotoro/internal/tailscale"
)

// TailscaleSetupResult holds VPN details after install + connect.
type TailscaleSetupResult struct {
	IP        string
	Tailnet   string
	MagicDNS  string
	NodeName  string
	ServeURL  string
	PublicURL string
}

type tailscaleFinalizeOpts struct {
	reuseExisting bool
}

// RunTailscaleSetup installs Tailscale if needed, then connects (env key or browser login).
// Set FOTORO_TAILSCALE_RESET=1 to remove an existing install and start fresh (forces browser login).
func RunTailscaleSetup(ts *tailscale.Manager) (*TailscaleSetupResult, error) {
	if ts == nil {
		ts = tailscale.NewManager()
	}

	forceReauth := os.Getenv("FOTORO_TAILSCALE_RESET") == "1"

	if forceReauth {
		fmt.Println("Removing existing Tailscale install (FOTORO_TAILSCALE_RESET=1)…")
		if err := ts.Reset(); err != nil {
			return nil, err
		}
		fmt.Println()
	}

	nodeName := os.Getenv("FOTORO_NODE_NAME")
	if nodeName == "" {
		nodeName = "fotoro-server"
	}
	ts.SetNodeName(nodeName)

	if ts.IsRunning() && !forceReauth {
		fmt.Println("Tailscale is already connected — reusing your existing VPN session.")
		return finalizeTailscale(ts, nodeName, tailscaleFinalizeOpts{reuseExisting: true})
	}

	fmt.Println("Tailscale gives this server a private IP and MagicDNS on your devices.")
	if os.Getenv("FOTORO_FUNNEL") == "1" {
		fmt.Println("Public HTTPS via Tailscale Funnel will be enabled after connect.")
	} else {
		fmt.Println("Optional public HTTPS: set FOTORO_FUNNEL=1")
	}
	fmt.Println()

	if !ts.IsInstalled() {
		fmt.Println("Installing Tailscale…")
		if err := ts.Install(); err != nil {
			return nil, err
		}
		fmt.Println()
	}

	fmt.Println("Connecting this machine to your Tailscale network…")

	if key := strings.TrimSpace(os.Getenv("TAILSCALE_AUTH_KEY")); key != "" {
		fmt.Println("Using TAILSCALE_AUTH_KEY from environment…")
		if err := ts.Up(key, []string{"tag:fotoro"}); err != nil {
			return nil, err
		}
		return finalizeTailscale(ts, nodeName, tailscaleFinalizeOpts{})
	}

	fmt.Println()
	if forceReauth {
		fmt.Println("A browser window will open — sign in with your Tailscale account.")
		fmt.Println("(Use the same Google/email you want this server on, or create a free account.)")
	} else {
		fmt.Println("A browser window will open for Tailscale login.")
		fmt.Println("New to Tailscale? Pick “Sign up” — the personal plan is free.")
	}
	fmt.Println()

	if err := ts.UpInteractive(forceReauth); err != nil {
		return nil, err
	}
	return finalizeTailscale(ts, nodeName, tailscaleFinalizeOpts{})
}

func finalizeTailscale(ts *tailscale.Manager, nodeName string, opts tailscaleFinalizeOpts) (*TailscaleSetupResult, error) {
	result, err := captureTailscaleResult(ts, nodeName)
	if err != nil {
		return nil, err
	}

	if result.MagicDNS != "" {
		result.PublicURL = tailscale.PublicURL(result.MagicDNS)
	}

	// Serve/funnel need the API listening on localhost — configured when you run server/app.
	fmt.Println()
	fmt.Println("Tailscale VPN is ready.")
	fmt.Println("Start the API to enable tailnet access:")
	fmt.Println("  ./fotoro server   or   ./fotoro app")
	if os.Getenv("FOTORO_FUNNEL") == "1" {
		fmt.Println("(Funnel will be enabled automatically when the server starts)")
	}

	return result, nil
}

func fotoroServePort() int {
	if addr := strings.TrimSpace(os.Getenv("FOTORO_ADDR")); addr != "" {
		if _, portStr, ok := strings.Cut(addr, ":"); ok {
			if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
				return p
			}
		}
	}
	return 8765
}

func captureTailscaleResult(ts *tailscale.Manager, nodeName string) (*TailscaleSetupResult, error) {
	ip, err := ts.GetTailscaleIP()
	if err != nil {
		return nil, fmt.Errorf("connected but could not read Tailscale IP: %w", err)
	}
	tailnet, _ := ts.GetTailnetName()
	magicDNS, _ := ts.GetMagicDNS()
	loginName, _ := ts.GetLoginName()

	fmt.Printf("✅ Tailscale ready\n")
	fmt.Printf("   IP:       %s\n", ip)
	if loginName != "" {
		fmt.Printf("   Account:  %s\n", loginName)
	}
	fmt.Printf("   Tailnet:  %s\n", tailnet)
	fmt.Printf("   MagicDNS: %s\n", magicDNS)
	if magicDNS != "" {
		fmt.Printf("   URL:      https://%s\n", magicDNS)
	}

	return &TailscaleSetupResult{
		IP:       ip,
		Tailnet:  tailnet,
		MagicDNS: magicDNS,
		NodeName: nodeName,
	}, nil
}

func persistTailscaleConfig(database *db.DB, localUserID int, ts *TailscaleSetupResult) {
	if ts == nil || database == nil {
		return
	}
	cfg := map[string]interface{}{
		"tailscale_enabled": 1,
		"tailscale_ip":      ts.IP,
		"tailnet_name":      ts.Tailnet,
		"tailnet_url":       ts.MagicDNS,
		"server_name":       ts.NodeName,
	}
	if ts.ServeURL != "" {
		cfg["tailnet_serve_url"] = ts.ServeURL
	}
	if ts.PublicURL != "" {
		cfg["public_url"] = ts.PublicURL
	}
	_ = database.UpdateServerConfig(cfg)
	_ = database.UpdateUserTailscale(localUserID, ts.IP, ts.Tailnet, ts.NodeName)
}
