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
	// Init DB
	db, err := sql.Open("sqlite", "fotoro.db")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	// CRITICAL: WAL mode + busy timeout for concurrent access
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")

	initSchema(db)

	// Collect images
	files := collectImages(dir)
	fmt.Printf("Found %d images\n", len(files))

	const workers = 4
	jobs := make(chan string, len(files))
	captionResults := make(chan captionResult, len(files))

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go captionWorker(w, jobs, captionResults, &wg)
	}

	// Feed jobs
	for _, f := range files {
		jobs <- f
	}
	close(jobs)

	// DB writer: SINGLE goroutine, no lock contention
	go func() {
		wg.Wait()
		close(captionResults)
	}()

	var processed, failed, skipped int
	start := time.Now()

	for res := range captionResults {
		if res.skipped {
			skipped++
			continue
		}
		if res.err != nil {
			failed++
			fmt.Printf("[FAIL] %s: %v\n", filepath.Base(res.path), res.err)
			continue
		}

		// SINGLE DB WRITER - no concurrency conflicts
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
			fmt.Printf("[WARN] FTS5 insert failed for %s: %v\n", filepath.Base(res.path), err)
		}

		processed++

		if processed%10 == 0 {
			elapsed := time.Since(start).Seconds()
			rate := float64(processed) / elapsed
			fmt.Printf("[PROGRESS] %d processed, %d failed, %d skipped | %.2f img/s\n", processed, failed, skipped, rate)
		}
	}

	total := time.Since(start)
	fmt.Printf("\n=== DONE ===\n")
	fmt.Printf("Processed: %d\n", processed)
	fmt.Printf("Failed: %d\n", failed)
	fmt.Printf("Skipped: %d\n", skipped)
	fmt.Printf("Total time: %.0fs (%.1f min)\n", total.Seconds(), total.Minutes())
	if processed > 0 {
		fmt.Printf("Average: %.1fs per image\n", total.Seconds()/float64(processed))
	}
}

type captionResult struct {
	path    string
	hash    string
	caption string
	err     error
	skipped bool
}

func captionWorker(id int, jobs <-chan string, results chan<- captionResult, wg *sync.WaitGroup) {
	defer wg.Done()

	for path := range jobs {
		img, err := validate.ProcessImage(path)
		if err != nil {
			results <- captionResult{path: path, err: err}
			continue
		}

		// Check if already in DB (we need a separate check, but for now skip)
		// For simplicity, we skip the duplicate check here and handle it at DB level
		// with INSERT OR IGNORE, or you can add a separate check before this.

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
