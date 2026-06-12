package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/validate"
)

func RunBackfillThumbs(dbPath string) {
	fmt.Println("[INIT] Opening database...")
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	cacheDir := filepath.Join(filepath.Dir(dbPath), ".cache")

	// Find images missing thumbnails
	rows, err := database.Query(`
		SELECT path, hash FROM images 
		WHERE hash NOT IN (
			SELECT hash FROM thumbnails WHERE size = 'small'
		)
	`)
	if err != nil {
		panic(err)
	}

	type item struct {
		path string
		hash string
	}
	var items []item
	for rows.Next() {
		var path, hash string
		rows.Scan(&path, &hash)
		items = append(items, item{path, hash})
	}
	rows.Close()

	if len(items) == 0 {
		fmt.Println("[INIT] All images have thumbnails. Nothing to do.")
		return
	}

	fmt.Printf("[INIT] %d images need thumbnails\n", len(items))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	var done int64
	start := time.Now()

	for _, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(path, hash string) {
			defer wg.Done()
			defer func() { <-sem }()

			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Printf("[SKIP] %s: file not found\n", hash[:8])
				return
			}

			meta, err := validate.PrepareImage(path)
			if err != nil {
				fmt.Printf("[FAIL] %s: %v\n", hash[:8], err)
				return
			}

			if err := validate.SaveThumbnails(cacheDir, meta); err != nil {
				fmt.Printf("[FAIL] %s: thumbs: %v\n", hash[:8], err)
				return
			}

			c := atomic.AddInt64(&done, 1)
			if c%50 == 0 {
				fmt.Printf("[OK] %d/%d thumbs done... (%.1fs)\n", c, len(items), time.Since(start).Seconds())
			}
		}(it.path, it.hash)
	}

	wg.Wait()
	fmt.Printf("\nDone. Generated %d thumbnails in %.1f minutes\n", done, time.Since(start).Minutes())
}
