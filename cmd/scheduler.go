package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fotoro/internal/db"
	jobs "fotoro/internal/scheduler"
)

func RunScheduler(dbPath, model string) {
	fmt.Println("[INIT] Opening database...")
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	queue := jobs.NewJobQueue(database, jobs.ScheduleConfig{
		Enabled:            false,
		ProcessTime:        "02:00",
		MaxConcurrent:      1,
		MaxPerBatch:        25,
		ProcessScreenshots: true,
		ProcessDocuments:   true,
		ProcessPhotos:      true,
	})
	if err := queue.InitDB(); err != nil {
		panic(err)
	}
	if err := queue.LoadConfig(); err != nil {
		fmt.Printf("[WARN] Could not load scheduler config: %v\n", err)
	}

	uploadsDir := filepath.Join(filepath.Dir(dbPath), "uploads")
	files := collectImages(uploadsDir)
	fmt.Printf("[INIT] Found %d images in uploads\n", len(files))

	var queued, duplicates, skipped int
	for _, path := range files {
		upload, err := queue.AddUpload(path, "scheduler", filepath.Base(path), contentTypeFromName(path), 0)
		if err != nil {
			if strings.Contains(err.Error(), "already queued") {
				skipped++
				continue
			}
			fmt.Printf("[QUEUE] [SKIP] %s: %v\n", filepath.Base(path), err)
			skipped++
			continue
		}
		if upload.Status == "duplicate" {
			_ = os.Remove(path)
			duplicates++
			continue
		}
		queued++
	}
	fmt.Printf("[INIT] Queued %d new images, %d duplicates, %d already queued/skipped\n", queued, duplicates, skipped)

	if err := queue.ProcessNow(model); err != nil {
		fmt.Printf("[ERROR] Scheduler failed: %v\n", err)
		return
	}
	fmt.Println("[DONE] Pending queue processed.")
}

func contentTypeFromName(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
