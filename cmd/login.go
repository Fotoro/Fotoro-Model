package cmd

import (
	"fmt"
	"os"
	"time"
)

func RunLogin(dbPath string) {
	addr := os.Getenv("FOTORO_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8765"
	}

	result, err := RunAuthPipeline(dbPath, addr, 5*time.Minute)
	if err != nil {
		fmt.Printf("Sign-in failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Session saved locally. Token preview:")
	if len(result.Token) > 24 {
		fmt.Printf("  %s…%s\n", result.Token[:12], result.Token[len(result.Token)-8:])
	} else {
		fmt.Printf("  %s\n", result.Token)
	}
	fmt.Printf("User ID: %s\n", result.User.ID)
	if result.Tailscale != nil {
		fmt.Printf("Tailscale IP: %s\n", result.Tailscale.IP)
		fmt.Printf("MagicDNS:     %s\n", result.Tailscale.MagicDNS)
	}
	fmt.Println()
	fmt.Println("You can now run: ./fotoro app  or  ./fotoro server")
}
