package cmd

import (
	"context"
	"fmt"
	"os"

	"fotoro/internal/db"
	"fotoro/internal/media"
)

func RunReconcileSizes(dbPath string) {
	LoadDotEnv()
	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer database.Close()
	service, err := media.NewService(database, dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	updated, err := service.ReconcileMediaSizes(context.Background(), 100000)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if updated > 0 {
		_, _ = database.Exec(`DELETE FROM dashboard_metrics WHERE id = 1`)
	}
	fmt.Printf("Measured %d media file sizes; originals were not changed.\n", updated)
}
