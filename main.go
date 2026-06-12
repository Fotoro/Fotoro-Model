package main

import (
	"fmt"
	"os"

	"fotoro/cmd"
	"fotoro/internal/system"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: fotoro [ingest|daemon|server|app|backfill|backfill-thumbs|system]")
		os.Exit(1)
	}

	dbPath := os.Getenv("FOTORO_DB")
	if dbPath == "" {
		dbPath = "fotoro.db"
	}
	model := os.Getenv("FOTORO_MODEL")
	if model == "" {
		model = "llava-phi3"
	}

	switch os.Args[1] {
	case "ingest":
		if len(os.Args) < 3 {
			fmt.Println("Usage: fotoro ingest <directory>")
			os.Exit(1)
		}
		cmd.RunIngest(os.Args[2], dbPath, model)
	case "daemon":
		watchDir := os.Getenv("FOTORO_WATCH_DIR")
		if watchDir == "" {
			watchDir = "./images"
		}
		cmd.RunDaemon(watchDir, dbPath, model)
	case "server":
		addr := os.Getenv("FOTORO_ADDR")
		if addr == "" {
			addr = ":8080"
		}
		cmd.RunServer(addr, dbPath, model)
	case "app":
		cmd.RunApp(dbPath, model)
	case "backfill":
		cmd.RunBackfill(dbPath, model)
	case "backfill-thumbs":
		cmd.RunBackfillThumbs(dbPath)
	case "system":
		specs, err := system.Detect()
		if err != nil {
			fmt.Printf("Error detecting system: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("══════════════════════════════════════════════════")
		fmt.Println("  Fotoro System Check")
		fmt.Println("══════════════════════════════════════════════════")
		fmt.Println(specs.String())
		fmt.Println("")
		fmt.Println("Recommended Configuration:")
		for k, v := range specs.RecommendConfig() {
			fmt.Printf("  %s=%s\n", k, v)
		}
		fmt.Println("══════════════════════════════════════════════════")
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
