package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/system"
)

func RunSetup(dbPath, model string) {
	LoadDotEnv()

	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  Fotoro Setup Wizard")
	fmt.Println("  No daemons. No auto-start. You control everything.")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()

	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Printf("Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	specs, err := system.Detect()
	if err != nil {
		fmt.Printf("Warning: could not detect system specs: %v\n", err)
	} else {
		fmt.Println("System detected:")
		fmt.Println(specs.String())
		fmt.Println()
	}

	addr := os.Getenv("FOTORO_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8765"
	}

	// Steps 1–2: Fotoro auth + Tailscale install/login (single pipeline)
	result, err := RunAuthPipeline(dbPath, addr, 5*time.Minute)
	if err != nil {
		fmt.Printf("Setup failed: %v\n", err)
		os.Exit(1)
	}

	if len(result.Token) > 24 {
		fmt.Printf("\nLocal account linked (ID: %d)\n", result.LocalUserID)
		fmt.Printf("Token received: %s…%s\n\n", result.Token[:12], result.Token[len(result.Token)-8:])
	}

	// Step 4: Directories
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  STEP 4 — Directories")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	baseDir := filepath.Dir(dbPath)
	uploadsDir := filepath.Join(baseDir, "uploads")
	cacheDir := filepath.Join(baseDir, ".cache")
	os.MkdirAll(uploadsDir, 0755)
	os.MkdirAll(cacheDir, 0755)
	os.MkdirAll(filepath.Join(cacheDir, "thumbnails", "small"), 0755)
	os.MkdirAll(filepath.Join(cacheDir, "thumbnails", "medium"), 0755)
	os.MkdirAll(filepath.Join(cacheDir, "previews"), 0755)
	fmt.Printf("✅ Directories created\n\n")

	database.UpdateServerConfig(map[string]interface{}{
		"setup_complete": 1,
		"setup_step":     "done",
	})

	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  ✅ Setup Complete!")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("Your Fotoro server is ready:\n")
	fmt.Printf("  Signed in as:   %s\n", result.User.Email)
	if result.Tailscale != nil {
		fmt.Printf("  Tailscale IP:   %s\n", result.Tailscale.IP)
		fmt.Printf("  MagicDNS:       %s\n", result.Tailscale.MagicDNS)
	}
	fmt.Printf("  Uploads folder: %s\n", uploadsDir)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  ./fotoro app       # Start the GUI")
	fmt.Println("  ./fotoro server    # Start API server only")
	fmt.Println("  ./fotoro nodesync  # Re-push node info to dashboard")
	fmt.Println()
	fmt.Println("To stop: Press Ctrl+C or close the window")
	fmt.Println("No background processes will remain running")
	fmt.Println()
}
