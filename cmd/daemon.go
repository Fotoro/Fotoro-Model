package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/ollama"
	"fotoro/internal/validate"

	"github.com/fsnotify/fsnotify"
)

func RunDaemon(watchDir, dbPath, model string) {
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	client := ollama.NewClient("http://localhost:11434", model)
	cacheDir := filepath.Join(filepath.Dir(dbPath), ".cache")

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	defer watcher.Close()

	// Recursively watch all subdirectories
	filepath.WalkDir(watchDir, func(path string, d os.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			watcher.Add(path)
		}
		return nil
	})

	fmt.Printf("Daemon watching: %s\n", watchDir)

	// Batch buffer: accumulate new files for 5 seconds then process
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	pending := make(map[string]struct{})

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == fsnotify.Create {
				info, err := os.Stat(event.Name)
				if err == nil && info.IsDir() {
					watcher.Add(event.Name)
					continue
				}
				if isImage(event.Name) {
					pending[event.Name] = struct{}{}
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Printf("Watcher error: %v\n", err)
		case <-ticker.C:
			if len(pending) == 0 {
				continue
			}
			batch := make([]string, 0, len(pending))
			for p := range pending {
				batch = append(batch, p)
				delete(pending, p)
			}
			processBatch(database, client, cacheDir, batch)
		}
	}
}

func processBatch(database *db.DB, client *ollama.Client, cacheDir string, paths []string) {
	fmt.Printf("[BATCH] Processing %d images\n", len(paths))
	for _, path := range paths {
		// Deduplication check
		var exists int
		h, _ := validate.FastHash(path)
		database.QueryRow("SELECT 1 FROM images WHERE hash = ?", h).Scan(&exists)
		if exists == 1 {
			continue
		}

		meta, err := validate.PrepareImage(path)
		if err != nil {
			fmt.Printf("[SKIP] %s: %v\n", filepath.Base(path), err)
			continue
		}

		_ = validate.SaveThumbnails(cacheDir, meta)

		analysis, err := client.AnalyzeImage(meta.VLMBytes)
		if err != nil {
			fmt.Printf("[FAIL] %s: %v\n", filepath.Base(path), err)
			continue
		}

		tags := ""
		if len(analysis.Tags) > 0 {
			tags = strings.Join(analysis.Tags, " ")
		}
		database.Exec(
			`INSERT INTO images (path, hash, caption, category, tags, has_text, has_faces, orientation, tier, processed_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			meta.Path, meta.Hash, analysis.Caption, analysis.Category,
			tags, boolToInt(analysis.HasText), boolToInt(analysis.HasFaces),
			analysis.Orientation, "daemon", time.Now(),
		)
		fmt.Printf("[OK] %s | %s\n", filepath.Base(path), analysis.Category)
	}
}

func isImage(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp"
}