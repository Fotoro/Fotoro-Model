package cmd

import (
	"fmt"
	"os"

	"fotoro/internal/db"
	"fotoro/internal/tailscale"
)

func RunTailscale(action string, dbPath string) {
	ts := tailscale.NewManager()

	switch action {
	case "status":
		fmt.Println(ts.StatusString())

	case "connect", "up":
		authKey := os.Getenv("TAILSCALE_AUTH_KEY")
		if authKey == "" {
			fmt.Print("Enter Tailscale auth key: ")
			fmt.Scanln(&authKey)
		}
		if authKey == "" {
			fmt.Println("Error: No auth key provided")
			fmt.Println("Get one at: https://login.tailscale.com/admin/settings/keys")
			os.Exit(1)
		}
		fmt.Println("[TAILSCALE] Connecting...")
		if err := ts.Up(authKey, []string{"tag:fotoro"}); err != nil {
			fmt.Printf("Failed: %v\\n", err)
			os.Exit(1)
		}
		database, _ := db.Open(dbPath)
		if database != nil {
			ip, _ := ts.GetTailscaleIP()
			tailnet, _ := ts.GetTailnetName()
			magicDNS, _ := ts.GetMagicDNS()
			database.UpdateServerConfig(map[string]interface{}{
				"tailscale_enabled": 1,
				"tailscale_ip":      ip,
				"tailnet_name":      tailnet,
				"tailnet_url":       magicDNS,
			})
			syncNodeFromDBQuiet(database)
			database.Close()
		}

	case "disconnect", "down":
		fmt.Println("[TAILSCALE] Disconnecting...")
		if err := ts.Down(); err != nil {
			fmt.Printf("Failed: %v\\n", err)
			os.Exit(1)
		}
		database, _ := db.Open(dbPath)
		if database != nil {
			database.UpdateServerConfig(map[string]interface{}{
				"tailscale_enabled": 0,
			})
			database.Close()
		}
		fmt.Println("Disconnected")

	case "info":
		ip, err := ts.GetTailscaleIP()
		if err != nil {
			fmt.Printf("Not connected: %v\\n", err)
			os.Exit(1)
		}
		tailnet, _ := ts.GetTailnetName()
		magicDNS, _ := ts.GetMagicDNS()
		fmt.Printf("IP:        %s\\n", ip)
		fmt.Printf("Tailnet:   %s\\n", tailnet)
		fmt.Printf("MagicDNS:  %s\\n", magicDNS)

	default:
		fmt.Printf("Unknown action: %s\\n", action)
		os.Exit(1)
	}
}
