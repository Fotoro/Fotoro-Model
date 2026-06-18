package cmd

import (
	"fmt"
	"os"

	"fotoro/internal/cloudsync"
	"fotoro/internal/db"
	"fotoro/internal/tailscale"
)

func syncNodeAfterSetup(database *db.DB, accessToken, ip, tailnet, magicDNS, nodeName string) {
	if accessToken == "" || ip == "" {
		return
	}
	if nodeName == "" {
		nodeName = "fotoro-server"
	}

	cfg, _ := database.GetServerConfig()
	publicURL := configString(cfg, "public_url")
	tailnetURL := configString(cfg, "tailnet_serve_url")
	if tailnetURL == "" {
		tailnetURL = publicURL
	}
	if publicURL == "" && magicDNS != "" {
		publicURL = "https://" + magicDNS
	}

	err := cloudsync.SyncNode(accessToken, cloudsync.NodeInfo{
		TailscaleIP: ip,
		TailnetName: tailnet,
		MagicDNS:    magicDNS,
		NodeName:    nodeName,
		PublicURL:   publicURL,
		TailnetURL:  tailnetURL,
	})
	if err != nil {
		fmt.Printf("[WARN] Could not register node with dashboard: %v\n", err)
		fmt.Println("[HINT] Dashboard sync needs network access to", cloudsync.WebAPIBase())
		return
	}
	fmt.Println("✅ Server registered on fotoro.vercel.app dashboard")
}

func syncNodeFromDBQuiet(database *db.DB) {
	if err := cloudsync.SyncNodeFromDB(database); err != nil {
		fmt.Printf("[WARN] Dashboard node sync: %v\n", err)
	}
}

func (s *Server) syncNodeToCloud() {
	if s.db == nil {
		return
	}
	if err := cloudsync.SyncNodeLive(s.db, s.tailscale); err != nil {
		fmt.Printf("[WARN] Dashboard node sync: %v\n", err)
	}
}

func RunNodeSyncCLI(dbPath string) {
	LoadDotEnv()
	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Printf("Database error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	ts := tailscale.NewManager()
	if err := cloudsync.SyncNodeLive(database, ts); err != nil {
		fmt.Printf("Sync failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Node synced to dashboard.")
}

func configString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
