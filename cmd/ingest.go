package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/ollama"
	"fotoro/internal/search"
	"fotoro/internal/validate"
)

func RunIngest(dir, dbPath, model string) {
	fmt.Println("[INIT] Opening database...")
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	fmt.Println("[INIT] Starting inference backend...")
	client := ollama.NewClient("", model)
	if err := client.HealthCheck(); err != nil {
		fmt.Printf("[INIT] Backend will start on first request (%v)\n", err)
	}

	embedClient := ollama.NewEmbedClient()
	if err := embedClient.HealthCheck(); err != nil {
		fmt.Println("[WARN] Embed server not running. Search will be FTS-only until you run: fotoro backfill")
	}

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

	var jobs []string
	for _, path := range files {
		hash, err := validate.FastHash(path)
		if err != nil {
			fmt.Printf("[PRE] [SKIP] %s: hash error: %v\n", filepath.Base(path), err)
			continue
		}
		if _, ok := existing[hash]; ok {
			fmt.Printf("[PRE] [SKIP] %s: duplicate\n", filepath.Base(path))
			continue
		}
		existing[hash] = struct{}{}
		jobs = append(jobs, path)
	}
	fmt.Printf("[INIT] %d new images to process\n", len(jobs))

	if len(jobs) == 0 {
		fmt.Println("[INIT] Nothing to do.")
		return
	}

	visionWorkers := 2
	if w := os.Getenv("FOTORO_VISION_WORKERS"); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 {
			visionWorkers = n
		}
	}
	
	llmWorkers := 1
	fmt.Printf("[INIT] Vision workers: %d | LLM workers: %d\n", visionWorkers, llmWorkers)

	type visionResult struct {
		path string
		meta *validate.ImageMeta
	}
	type llmResult struct {
		path     string
		meta     *validate.ImageMeta
		analysis ollama.Analysis
	}

	visionQ := make(chan string, len(jobs))
	llmQ := make(chan visionResult, 100)
	dbQ := make(chan llmResult, 100)

	var processed, skipped, failed int64
	start := time.Now()

	var visionWg sync.WaitGroup
	for i := 0; i < visionWorkers; i++ {
		visionWg.Add(1)
		go func(id int) {
			defer visionWg.Done()
			for path := range visionQ {
				meta, err := validate.PrepareImage(path)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					fmt.Printf("[V%d] [SKIP] %s: prep error: %v\n", id, filepath.Base(path), err)
					continue
				}

				if meta.PHash != "" {
					var dupHash string
					database.QueryRow("SELECT hash FROM images WHERE phash = ?", meta.PHash).Scan(&dupHash)
					if dupHash != "" {
						fmt.Printf("[V%d] [SKIP] %s: perceptual duplicate of %s\n", id, filepath.Base(path), dupHash[:8])
						atomic.AddInt64(&skipped, 1)
						continue
					}
				}

				if err := validate.SaveThumbnails(cacheDir, meta); err != nil {
					fmt.Printf("[V%d] [WARN] %s: thumbs: %v\n", id, filepath.Base(path), err)
				}
				llmQ <- visionResult{path: path, meta: meta}
			}
		}(i)
	}

	var llmWg sync.WaitGroup
	llmWg.Add(1)
	go func() {
		defer llmWg.Done()
		for res := range llmQ {
			t0 := time.Now()
			analysis, err := client.AnalyzeImage(res.meta.VLMBytes)
			if err != nil {
				atomic.AddInt64(&failed, 1)
				fmt.Printf("[LLM] [FAIL] %s: %v\n", filepath.Base(res.path), err)
				database.Exec(
					"INSERT OR IGNORE INTO images (path, hash, caption, category, tier, taken_at, phash) VALUES (?, ?, ?, ?, ?, ?, ?)",
					res.meta.Path, res.meta.Hash, "", "failed", "bulk", res.meta.TakenAt, res.meta.PHash,
				)
				continue
			}

			fmt.Printf("[LLM] [OK] %s | %.70s (%.1fs)\n",
				filepath.Base(res.path), analysis.Caption, time.Since(t0).Seconds())
			dbQ <- llmResult{path: res.path, meta: res.meta, analysis: analysis}
		}
	}()

	var dbWg sync.WaitGroup
	dbWg.Add(1)
	go func() {
		defer dbWg.Done()
		for res := range dbQ {
			tags := ""
			if len(res.analysis.Tags) > 0 {
				tags = strings.Join(res.analysis.Tags, " ")
			}
			_, err := database.Exec(
				`INSERT INTO images (path, hash, caption, category, tags, has_text, has_faces, orientation, tier, processed_at, taken_at, phash)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				res.meta.Path, res.meta.Hash, res.analysis.Caption, res.analysis.Category,
				tags, boolToInt(res.analysis.HasText), boolToInt(res.analysis.HasFaces),
				res.analysis.Orientation, "bulk", time.Now(), res.meta.TakenAt, res.meta.PHash,
			)
			if err != nil {
				atomic.AddInt64(&failed, 1)
				fmt.Printf("[DB] [FAIL] %s: %v\n", filepath.Base(res.path), err)
				continue
			}
			atomic.AddInt64(&processed, 1)

			if emb, err := embedClient.GetEmbedding(res.analysis.Caption); err == nil {
				blob := search.FloatsToBytes(emb)
				database.Exec("UPDATE images SET embedding = ? WHERE hash = ?", blob, res.meta.Hash)
			} else {
				fmt.Printf("[EMBED] [FAIL] %s: %v\n", filepath.Base(res.path), err)
			}

			p := atomic.LoadInt64(&processed)
			f := atomic.LoadInt64(&failed)
			s := atomic.LoadInt64(&skipped)
			total := p + f + s
			elapsed := time.Since(start).Seconds()
			rate := float64(total) / elapsed
			fmt.Printf("  Progress: %d OK, %d skip, %d fail | %.2f img/s | elapsed: %.0fs\n",
				p, s, f, rate, elapsed)
		}
	}()

	for _, path := range jobs {
		visionQ <- path
	}
	close(visionQ)
	visionWg.Wait()
	close(llmQ)
	llmWg.Wait()
	close(dbQ)
	dbWg.Wait()

	total := time.Since(start)
	p := atomic.LoadInt64(&processed)
	f := atomic.LoadInt64(&failed)
	s := atomic.LoadInt64(&skipped)
	fmt.Printf("\n=== DONE ===\n")
	fmt.Printf("Vision workers: %d | LLM workers: %d\n", visionWorkers, llmWorkers)
	fmt.Printf("Processed: %d | Skipped: %d | Failed: %d\n", p, s, f)
	if len(jobs) > 0 {
		fmt.Printf("Total time: %.1f minutes (%.1f sec/image)\n",
			total.Minutes(), total.Seconds()/float64(len(jobs)))
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
