package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/ollama"
	"fotoro/internal/search"
	"fotoro/internal/validate"

	"github.com/fsnotify/fsnotify"
)

func RunDaemon(watchDir, dbPath, model string) {
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	client := ollama.NewClient("", model)
	embedClient := ollama.NewEmbedClient()
	cacheDir := filepath.Join(filepath.Dir(dbPath), ".cache")

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	defer watcher.Close()

	filepath.WalkDir(watchDir, func(path string, d os.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			watcher.Add(path)
		}
		return nil
	})

	fmt.Printf("Daemon watching: %s\n", watchDir)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	pending := make(map[string]struct{})
	var pendingMu sync.Mutex

	type visionResult struct {
		path string
		meta *validate.ImageMeta
	}
	type llmResult struct {
		path     string
		meta     *validate.ImageMeta
		analysis ollama.Analysis
	}

	visionQ := make(chan string, 100)
	llmQ := make(chan visionResult, 100)
	dbQ := make(chan llmResult, 100)

	var visionWg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		visionWg.Add(1)
		go func() {
			defer visionWg.Done()
			for path := range visionQ {
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
				if meta.PHash != "" {
					var dupHash string
					database.QueryRow("SELECT hash FROM images WHERE phash = ?", meta.PHash).Scan(&dupHash)
					if dupHash != "" {
						fmt.Printf("[SKIP] %s: perceptual duplicate of %s\n", filepath.Base(path), dupHash[:8])
						continue
					}
				}
				_ = validate.SaveThumbnails(cacheDir, meta)
				llmQ <- visionResult{path: path, meta: meta}
			}
		}()
	}

	go func() {
		for res := range llmQ {
			t0 := time.Now()
			analysis, err := client.AnalyzeImage(res.meta.VLMBytes)
			if err != nil {
				fmt.Printf("[FAIL] %s: %v\n", filepath.Base(res.path), err)
				continue
			}
			fmt.Printf("[LLM] %s | %s (%.1fs)\n", filepath.Base(res.path), analysis.Category, time.Since(t0).Seconds())
			dbQ <- llmResult{path: res.path, meta: res.meta, analysis: analysis}
		}
	}()

	go func() {
		for res := range dbQ {
			tags := ""
			if len(res.analysis.Tags) > 0 {
				tags = strings.Join(res.analysis.Tags, " ")
			}
			database.Exec(
				`INSERT INTO images (path, hash, caption, category, tags, has_text, has_faces, orientation, tier, processed_at, taken_at, phash)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				res.meta.Path, res.meta.Hash, res.analysis.Caption, res.analysis.Category,
				tags, boolToInt(res.analysis.HasText), boolToInt(res.analysis.HasFaces),
				res.analysis.Orientation, "daemon", time.Now(), res.meta.TakenAt, res.meta.PHash,
			)
			if emb, err := embedClient.GetEmbedding(res.analysis.Caption); err == nil {
				blob := search.FloatsToBytes(emb)
				database.Exec("UPDATE images SET embedding = ? WHERE hash = ?", blob, res.meta.Hash)
			}
			fmt.Printf("[OK] %s | %s\n", filepath.Base(res.path), res.analysis.Category)
		}
	}()

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
					pendingMu.Lock()
					pending[event.Name] = struct{}{}
					pendingMu.Unlock()
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Printf("Watcher error: %v\n", err)
		case <-ticker.C:
			pendingMu.Lock()
			if len(pending) == 0 {
				pendingMu.Unlock()
				continue
			}
			batch := make([]string, 0, len(pending))
			for p := range pending {
				batch = append(batch, p)
				delete(pending, p)
			}
			pendingMu.Unlock()
			fmt.Printf("[BATCH] Processing %d images\n", len(batch))
			for _, path := range batch {
				visionQ <- path
			}
		}
	}
}

func isImage(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp"
}
