package main

import (
	"fmt"
	"os"

	"fotoro/cmd"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: fotoro [ingest|daemon|server]")
		os.Exit(1)
	}

	dbPath := os.Getenv("FOTORO_DB")
	if dbPath == "" {
		dbPath = "fotoro.db"
	}
	model := os.Getenv("FOTORO_MODEL")
	if model == "" {
		model = "qwen2.5-vl-3b-fotoro"
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
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}