package cmd

import (
	"fmt"
	"os"

	"fotoro/internal/db"
	"fotoro/internal/tailscale"
)

// RunTailscaleReset removes Tailscale from the system and clears local VPN config.
func RunTailscaleReset(dbPath string) {
	LoadDotEnv()

	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  Tailscale reset")
	fmt.Println("  Stops VPN, removes package, clears local Fotoro tailscale config")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()

	ts := tailscale.NewManager()
	if err := ts.Reset(); err != nil {
		fmt.Printf("Reset failed: %v\n", err)
		os.Exit(1)
	}

	if dbPath != "" {
		database, err := db.Open(dbPath)
		if err == nil {
			clearTailscaleLocalConfig(database)
			database.Close()
			fmt.Println("✅ Local Fotoro tailscale settings cleared")
		}
	}

	fmt.Println()
	fmt.Println("Tailscale removed. Run a fresh install with:")
	fmt.Println("  ./fotoro setup")
	fmt.Println("or:")
	fmt.Println("  FOTORO_TAILSCALE_RESET=0 ./fotoro login")
	fmt.Println()
}

func clearTailscaleLocalConfig(database *db.DB) {
	_ = database.UpdateServerConfig(map[string]interface{}{
		"tailscale_enabled": 0,
		"tailscale_ip":      "",
		"tailnet_name":      "",
		"tailnet_url":       "",
		"tailnet_serve_url": "",
		"public_url":        "",
		"funnel_enabled":    0,
		"serve_enabled":     0,
	})
}
