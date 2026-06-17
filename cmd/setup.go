package cmd

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fotoro/internal/db"
	"fotoro/internal/system"
	"fotoro/internal/tailscale"
)

func RunSetup(dbPath, model string) {
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  Fotoro Setup Wizard")
	fmt.Println("  No daemons. No auto-start. You control everything.")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()

	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Printf("Failed to open database: %v\\n", err)
		os.Exit(1)
	}
	defer database.Close()

	specs, err := system.Detect()
	if err != nil {
		fmt.Printf("Warning: could not detect system specs: %v\\n", err)
	} else {
		fmt.Println("System detected:")
		fmt.Println(specs.String())
		fmt.Println()
	}

	reader := bufio.NewReader(os.Stdin)

	// Step 1: User Account
	fmt.Println("STEP 1: Create Admin Account")
	fmt.Println("──────────────────────────────────────────────────────────────────")
	fmt.Print("Email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)
	fmt.Print("Name: ")
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)
	fmt.Print("Password: ")
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)
	passwordHash := fmt.Sprintf("%x", sha256.Sum256([]byte(password)))
	userID, err := database.CreateUser(email, name, passwordHash)
	if err != nil {
		fmt.Printf("Failed to create user: %v\\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ User created (ID: %d)\\n\\n", userID)

	// Step 2: Tailscale Setup
	fmt.Println("STEP 2: Tailscale Network Setup")
	fmt.Println("──────────────────────────────────────────────────────────────────")
	ts := tailscale.NewManager()
	if !ts.IsInstalled() {
		fmt.Println("Tailscale is not installed. Install it first:")
		fmt.Println("  curl -fsSL https://tailscale.com/install.sh | sh")
		fmt.Println()
		fmt.Print("Press Enter after installing Tailscale...")
		reader.ReadString('\n')
	}
	if !ts.IsRunning() {
		fmt.Println("Tailscale is not connected. Let's set it up.")
		fmt.Print("Do you have a Tailscale auth key? (y/n): ")
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) == "y" {
			fmt.Print("Enter auth key: ")
			authKey, _ := reader.ReadString('\n')
			authKey = strings.TrimSpace(authKey)
			fmt.Println("Connecting to Tailscale...")
			if err := ts.Up(authKey, []string{"tag:fotoro"}); err != nil {
				fmt.Printf("Failed to connect: %v\\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Println("Please create an auth key at https://login.tailscale.com/admin/settings/keys")
			fmt.Println("Then run: sudo tailscale up --auth-key=<your-key>")
			fmt.Println()
			fmt.Print("Press Enter after connecting...")
			reader.ReadString('\n')
		}
	}
	ip, err := ts.GetTailscaleIP()
	if err != nil {
		fmt.Printf("Failed to get Tailscale IP: %v\\n", err)
		os.Exit(1)
	}
	tailnet, _ := ts.GetTailnetName()
	magicDNS, _ := ts.GetMagicDNS()
	fmt.Printf("✅ Tailscale connected!\\n")
	fmt.Printf("   IP:       %s\\n", ip)
	fmt.Printf("   Tailnet:  %s\\n", tailnet)
	fmt.Printf("   MagicDNS: %s\\n\\n", magicDNS)
	database.UpdateServerConfig(map[string]interface{}{
		"tailscale_enabled": 1,
		"tailscale_ip":      ip,
		"tailnet_name":      tailnet,
		"server_name":       "fotoro-server",
	})
	database.UpdateUserTailscale(int(userID), ip, tailnet, "fotoro-server")

	// Step 3: Directories
	fmt.Println("STEP 3: Directories")
	fmt.Println("──────────────────────────────────────────────────────────────────")
	baseDir := filepath.Dir(dbPath)
	uploadsDir := filepath.Join(baseDir, "uploads")
	cacheDir := filepath.Join(baseDir, ".cache")
	os.MkdirAll(uploadsDir, 0755)
	os.MkdirAll(cacheDir, 0755)
	os.MkdirAll(filepath.Join(cacheDir, "thumbnails", "small"), 0755)
	os.MkdirAll(filepath.Join(cacheDir, "thumbnails", "medium"), 0755)
	os.MkdirAll(filepath.Join(cacheDir, "previews"), 0755)
	fmt.Printf("✅ Directories created\\n\\n")

	// Mark setup complete
	database.UpdateServerConfig(map[string]interface{}{
		"setup_complete": 1,
		"setup_step":     "done",
	})

	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  ✅ Setup Complete!")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("Your Fotoro server is ready:\n")
	fmt.Printf("  Tailscale IP:   %s\\n", ip)
	fmt.Printf("  MagicDNS:       %s\\n", magicDNS)
	fmt.Printf("  Uploads folder: %s\\n", uploadsDir)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  ./fotoro app     # Start the GUI")
	fmt.Println("  ./fotoro server  # Start API server only")
	fmt.Println()
	fmt.Println("To stop: Press Ctrl+C or close the window")
	fmt.Println("No background processes will remain running")
	fmt.Println()
}
