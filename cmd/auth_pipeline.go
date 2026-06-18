package cmd

import (
	"fmt"
	"os"
	"time"

	"fotoro/internal/auth"
	"fotoro/internal/cloudsync"
	"fotoro/internal/db"
	"fotoro/internal/tailscale"
)

// AuthPipelineResult is returned after Fotoro sign-in + Tailscale setup.
type AuthPipelineResult struct {
	Token        string
	User         *auth.User
	LocalUserID  int64
	Tailscale    *TailscaleSetupResult
}

// RunAuthPipeline: (1) Fotoro sign-in, (2) Tailscale, (3) register node on dashboard.
func RunAuthPipeline(dbPath, addr string, timeout time.Duration) (*AuthPipelineResult, error) {
	LoadDotEnv()

	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  STEP 1 / 3 — Fotoro account")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()

	token, user, err := RunWebSignIn(dbPath, addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("fotoro sign-in: %w", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	localUserID, err := ensureLocalUser(database, user)
	if err != nil {
		return nil, fmt.Errorf("link local account: %w", err)
	}

	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  STEP 2 / 3 — Tailscale VPN")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()

	var tsResult *TailscaleSetupResult
	if skipTailscalePipeline() {
		fmt.Println("(Skipping Tailscale — FOTORO_SKIP_TAILSCALE=1)")
		ts := tailscale.NewManager()
		if ts.IsRunning() {
			nodeName := os.Getenv("FOTORO_NODE_NAME")
			if nodeName == "" {
				nodeName = "fotoro-server"
			}
			tsResult, _ = captureTailscaleResult(ts, nodeName)
		}
	} else {
		if os.Getenv("FOTORO_TAILSCALE_RESET") == "1" {
			clearTailscaleLocalConfig(database)
		}
		tsResult, err = RunTailscaleSetup(tailscale.NewManager())
		if err != nil {
			return nil, fmt.Errorf("tailscale setup: %w", err)
		}
		persistTailscaleConfig(database, int(localUserID), tsResult)
	}

	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  STEP 3 / 3 — Link server to your account")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()

	if tsResult != nil && tsResult.IP != "" {
		syncNodeAfterSetup(database, token, tsResult.IP, tsResult.Tailnet, tsResult.MagicDNS, tsResult.NodeName)
	} else if err := syncNodeFromPipeline(database); err != nil {
		fmt.Printf("[WARN] Could not register node: %v\n", err)
		fmt.Println("[HINT] Connect Tailscale (./fotoro tailscale connect) then run ./fotoro nodesync")
	} else {
		fmt.Println("✅ Server registered on fotoro.vercel.app dashboard")
	}

	return &AuthPipelineResult{
		Token:       token,
		User:        user,
		LocalUserID: localUserID,
		Tailscale:   tsResult,
	}, nil
}

func syncNodeFromPipeline(database *db.DB) error {
	ts := tailscale.NewManager()
	if !ts.IsRunning() {
		return fmt.Errorf("tailscale not connected")
	}
	return cloudsync.SyncNodeFromDB(database)
}

func skipTailscalePipeline() bool {
	return os.Getenv("FOTORO_SKIP_TAILSCALE") == "1"
}
