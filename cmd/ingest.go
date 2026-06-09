package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/ollama"
	"fotoro/internal/validate"
)

func RunIngest(dir, dbPath, model string) {
	fmt.Println("[INIT] Opening database...")
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	fmt.Println("[INIT] Connecting to Ollama...")
	client := ollama.NewClient("http://localhost:11434", model)
	if err := client.HealthCheck(); err != nil {
		fmt.Fprintf(os.Stderr, "Ollama not running. Start it first: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[INIT] Ollama is healthy")

	fmt.Println("[INIT] Checking model...")
	if err := client.VerifyModel(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[INIT] Model '%s' found\n", model)

	files := collectImages(dir)
	fmt.Printf("[INIT] Found %d image files\n", len(files))

	existing := make(map[string]struct{})
	rows, _ := database.Query("SELECT hash FROM images")
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var h string
			rows.Scan(&h)
			existing[h] = struct{}{}
		}
	}
	fmt.Printf("[INIT] %d images already in database\n", len(existing))

	cacheDir := filepath.Join(filepath.Dir(dbPath), ".cache")

	workers := runtime.NumCPU()
	if w := os.Getenv("FOTORO_WORKERS"); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 {
			workers = n
		}
	}
	fmt.Printf("[INIT] Starting %d workers\n", workers)

	var mu sync.Mutex
	processed, skipped, failed := 0, 0, 0
	start := time.Now()

	jobs := make(chan string, len(files))
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for path := range jobs {

				hash, err := validate.FastHash(path)
				if err != nil {
					mu.Lock()
					failed++
					mu.Unlock()
					fmt.Printf("[W%d] [SKIP] %s: hash error: %v\n", id, filepath.Base(path), err)
					continue
				}

				mu.Lock()
				if _, ok := existing[hash]; ok {
					skipped++
					mu.Unlock()
					fmt.Printf("[W%d] [SKIP] %s: duplicate\n", id, filepath.Base(path))
					continue
				}
				existing[hash] = struct{}{}
				mu.Unlock()

				meta, err := validate.PrepareImage(path)
				if err != nil {
					mu.Lock()
					failed++
					mu.Unlock()
					fmt.Printf("[W%d] [SKIP] %s: prep error: %v\n", id, filepath.Base(path), err)
					continue
				}
				_ = validate.SaveThumbnails(cacheDir, meta)

				fmt.Printf("[W%d] → %s: Ollama...\n", id, filepath.Base(path))
				analysis, err := client.AnalyzeImage(meta.VLMBytes)
				if err != nil {
					mu.Lock()
					failed++
					mu.Unlock()
					fmt.Printf("[W%d] [FAIL] %s: %v\n", id, filepath.Base(path), err)
					database.Exec(
						"INSERT OR IGNORE INTO images (path, hash, caption, category, tier) VALUES (?, ?, ?, ?, ?)",
						meta.Path, meta.Hash, "", "failed", "bulk",
					)
					continue
				}

				

				// We don't care about tags anymore, hardcode the defaults for the unused columns
				_, err = database.Exec(
					`INSERT INTO images (path, hash, caption, category, tags, has_text, has_faces, orientation, tier, processed_at)
					 VALUES (?, ?, ?, 'unknown', '', 0, 0, 'unknown', 'bulk', ?)`,
					meta.Path, meta.Hash, analysis.Caption, time.Now(),
				)
				if err != nil {
					mu.Lock()
					failed++
					mu.Unlock()
					fmt.Printf("[W%d] [FAIL] %s: DB error: %v\n", id, filepath.Base(path), err)
					continue
				}

				mu.Lock()
				processed++
				total := processed + skipped + failed
				elapsed := time.Since(start).Seconds()
				rate := float64(total) / elapsed
				mu.Unlock()

				// Simplified progress print
				fmt.Printf("[W%d] [OK] %s | %.70s\n", id, filepath.Base(path), analysis.Caption)
				fmt.Printf("  Progress: %d OK, %d skip, %d fail | %.2f img/s | elapsed: %.0fs\n",
					processed, skipped, failed, rate, elapsed)
			}
		}(i)
	}

	for _, path := range files {
		jobs <- path
	}
	close(jobs)
	wg.Wait()

	total := time.Since(start)
	fmt.Printf("\n=== DONE ===\n")
	fmt.Printf("Workers: %d | Processed: %d | Skipped: %d | Failed: %d\n", workers, processed, skipped, failed)
	if len(files) > 0 {
		fmt.Printf("Total time: %.1f minutes (%.1f sec/image)\n", total.Minutes(), total.Seconds()/float64(len(files)))
	}
}

func collectImages(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
