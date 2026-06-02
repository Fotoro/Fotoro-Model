package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fotoro/internal/api"
	"fotoro/internal/validate"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: fotoro ingest <photo-directory>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "ingest":
		if len(os.Args) < 3 {
			fmt.Println("Usage: fotoro ingest <photo-directory>")
			os.Exit(1)
		}
		runIngest(os.Args[2])
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runIngest(dir string) {
	db, err := sql.Open("sqlite", "fotoro.db")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")

	initSchema(db)

	files := collectImages(dir)
	fmt.Printf("Found %d images\n", len(files))

	// 2 WORKERS = stable. 4 workers = timeouts on CPU.
	const workers = 3
	jobs := make(chan string, workers) // Small buffer = natural backpressure
	results := make(chan captionResult, len(files))

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go captionWorker(w, jobs, results, db, &wg)
	}

	// Feed jobs
	go func() {
		for _, f := range files {
			jobs <- f
		}
		close(jobs)
	}()

	// Close results when done
	go func() {
		wg.Wait()
		close(results)
	}()

	var processed, failed, skipped int
	start := time.Now()

	for res := range results {
		if res.skipped {
			skipped++
			continue
		}
		if res.err != nil {
			failed++
			fmt.Printf("[FAIL] %s: %v\n", filepath.Base(res.path), res.err)
			continue
		}

		_, err = db.Exec(
			"INSERT INTO images (path, hash, caption, tier) VALUES (?, ?, ?, ?)",
			res.path, res.hash, res.caption, "bulk",
		)
		if err != nil {
			failed++
			fmt.Printf("[FAIL] %s: db insert: %v\n", filepath.Base(res.path), err)
			continue
		}
		_, err = db.Exec(
			"INSERT INTO fts_captions (path, caption) VALUES (?, ?)",
			res.path, res.caption,
		)
		if err != nil {
			fmt.Printf("[WARN] FTS5 failed: %v\n", err)
		}

		processed++

		if processed%10 == 0 {
			elapsed := time.Since(start).Seconds()
			rate := float64(processed) / elapsed
			fmt.Printf("[PROGRESS] %d done, %d failed, %d skip | %.2f img/s | %.1fs/img\n",
				processed, failed, skipped, rate, 1.0/rate)
		}
	}

	total := time.Since(start)
	fmt.Printf("\n=== DONE ===\n")
	fmt.Printf("Processed: %d | Failed: %d | Skipped: %d\n", processed, failed, skipped)
	fmt.Printf("Time: %.0fs (%.1f min) | Avg: %.1fs/img\n", total.Seconds(), total.Minutes(), total.Seconds()/float64(processed))
}

type captionResult struct {
	path    string
	hash    string
	caption string
	err     error
	skipped bool
}

func captionWorker(id int, jobs <-chan string, results chan<- captionResult, db *sql.DB, wg *sync.WaitGroup) {
	defer wg.Done()

	for path := range jobs {
		img, err := validate.ProcessImage(path)
		if err != nil {
			results <- captionResult{path: path, err: err}
			continue
		}

		var exists int
		err = db.QueryRow("SELECT 1 FROM images WHERE hash = ?", img.Hash).Scan(&exists)
		if err == nil && exists == 1 {
			results <- captionResult{path: path, skipped: true}
			continue
		}

		caption, err := api.CaptionImage(img.JPEGBytes)
		if err != nil {
			results <- captionResult{path: path, err: err}
			continue
		}

		results <- captionResult{
			path:    img.Path,
			hash:    img.Hash,
			caption: caption,
		}
	}
}

func initSchema(db *sql.DB) {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS images (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL,
			hash TEXT UNIQUE NOT NULL,
			caption TEXT NOT NULL,
			tier TEXT DEFAULT 'bulk',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS fts_captions USING fts5(path, caption)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			panic(fmt.Sprintf("schema init failed: %v\nStatement: %s", err, stmt))
		}
	}
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_hash ON images(hash)`)
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
