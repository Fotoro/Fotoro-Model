package cmd

import (
	"context"
	"fmt"

	"fotoro/internal/db"
	"fotoro/internal/media"
)

func RunBackfillDetail(dbPath string, limit int) error {
	database, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer database.Close()
	service, err := media.NewService(database, dbPath)
	if err != nil {
		return err
	}
	completed, err := service.BackfillLargePreviews(context.Background(), limit)
	if err != nil {
		return err
	}
	fmt.Printf("Built %d display previews; original images were not changed.\n", completed)
	return nil
}
