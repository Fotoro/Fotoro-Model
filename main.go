package main

import (
	"fmt"
	"os"
	"strconv"

	"fotoro/cmd"
)

func main() {
	cmd.LoadDotEnv()

	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	dbPath := getEnv("FOTORO_DB", "./fotoro.db")
	model := getEnv("FOTORO_MODEL", "qwen2.5-vl-3b")

	switch os.Args[1] {
	case "ingest":
		if len(os.Args) < 3 {
			fmt.Println("Usage: fotoro ingest <directory>")
			os.Exit(1)
		}
		cmd.RunIngest(os.Args[2], dbPath, model)
	case "server":
		addr := getEnv("FOTORO_ADDR", "127.0.0.1:8765")
		cmd.RunServer(addr, dbPath, model)
	case "app":
		cmd.RunApp(dbPath, model)
	case "backfill":
		cmd.RunBackfill(dbPath, model)
	case "backfill-thumbs":
		cmd.RunBackfillThumbs(dbPath)
	case "reconcile-sizes":
		cmd.RunReconcileSizes(dbPath)
	case "backfill-detail":
		limit := 25
		if len(os.Args) > 2 {
			var err error
			limit, err = strconv.Atoi(os.Args[2])
			if err != nil || limit < 1 || limit > 1000 {
				fmt.Fprintln(os.Stderr, "Usage: fotoro backfill-detail [1-1000 images]")
				os.Exit(1)
			}
		}
		if err := cmd.RunBackfillDetail(dbPath, limit); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "system":
		cmd.RunSystemCheck()
	case "setup":
		cmd.RunSetup(dbPath, model)
	case "login":
		cmd.RunLogin(dbPath)
	case "scheduler":
		cmd.RunScheduler(dbPath, model)
	case "tailscale":
		if len(os.Args) < 3 {
			fmt.Println(`Tailscale operations:
  fotoro tailscale status     # Show connection status
  fotoro tailscale connect    # Connect to tailnet
  fotoro tailscale disconnect # Disconnect from tailnet
  fotoro tailscale info       # Show IP and DNS info`)
			os.Exit(0)
		}
		cmd.RunTailscale(os.Args[2], dbPath)
	case "nodesync":
		cmd.RunNodeSyncCLI(dbPath)
	default:
		fmt.Printf("Unknown command: %s\\n", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println(`Fotoro — Self-hosted photo intelligence

Usage: fotoro [command]

Commands:
  app          Launch desktop GUI (backend + Qt6 interface)
  server       Run HTTP API in foreground (advanced)
  ingest       Batch process images from directory
  backfill     Generate embeddings for existing images
  backfill-thumbs  Generate missing thumbnails
  reconcile-sizes  Measure legacy media sizes for accurate storage totals
  backfill-detail [count]  Build 2048 px viewing copies of older images (default: 25)
  scheduler    Run scheduled processing (manual)
  setup        Interactive first-time setup wizard
  login        Sign in via fotoro.vercel.app (links account to this install)
  nodesync     Push Tailscale/server info to the website dashboard
  system       Show system specs and recommendations
  tailscale    Tailscale operations (connect/disconnect/status)

Environment:
  FOTORO_DB          Database path (default: ./fotoro.db)
  FOTORO_ADDR        Server bind address (default: 127.0.0.1:8765)
  FOTORO_MODEL       VLM model name (default: qwen2.5-vl-3b)
  SUPABASE_URL       Supabase project URL
  SUPABASE_ANON_KEY  Supabase anon key
  GOOGLE_CLIENT_ID   Google OAuth client ID
  FOTORO_AUTH_URL    Hosted login page (default: https://fotoro.vercel.app/login)
  FOTORO_WEB_URL     Website origin for dashboard sync (default: derived from FOTORO_AUTH_URL)

Examples:
  fotoro app                 # Start GUI
  ./fotoroctl start           # Turn on on-demand API
  ./fotoroctl stop            # Turn off API and socket
  fotoro ingest ./photos     # Process photos directory
  fotoro scheduler           # Run pending queue now`)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
