package cmd

import (
	"fmt"
	"os"

	"fotoro/internal/system"
)

func RunSystemCheck() {
	specs, err := system.Detect()
	if err != nil {
		fmt.Printf("Error detecting system: %v\\n", err)
		os.Exit(1)
	}
	fmt.Println("══════════════════════════════════════════════════")
	fmt.Println("  Fotoro System Check")
	fmt.Println("══════════════════════════════════════════════════")
	fmt.Println(specs.String())
	fmt.Println("")
	fmt.Println("Recommended Configuration:")
	for k, v := range specs.RecommendConfig() {
		fmt.Printf("  %s=%s\\n", k, v)
	}
	fmt.Println("══════════════════════════════════════════════════")
}
