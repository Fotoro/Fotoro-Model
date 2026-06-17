package cmd

import (
	"fmt"
	"strings"
	"path/filepath"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/ollama"
	"fotoro/internal/search"
	"fotoro/internal/validate"
)

func RunScheduler(dbPath, model string) {
	fmt.Println("[INIT] Opening database...")
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	fmt.Println("[INIT] Starting inference backend...")
	client := ollama.NewClient("", model)
	if err := client.HealthCheck(); err != nil {
		fmt.Printf("[INIT] Backend will start on first request (%v)\\n", err)
	}

	embedClient := ollama.NewEmbedClient()
	if err := embedClient.HealthCheck(); err != nil {
		fmt.Println("[WARN] Embed server not running. Search will be FTS-only.")
	}

	cacheDir := filepath.Join(filepath.Dir(dbPath), ".cache")

	// Find pending images in uploads directory
	uploadsDir := filepath.Join(filepath.Dir(dbPath), "uploads")
	files := collectImages(uploadsDir)
	fmt.Printf("[INIT] Found %d images in uploads\\n", len(files))

	if len(files) == 0 {
		fmt.Println("[INIT] Nothing to process.")
		return
	}

	for _, path := range files {
		fmt.Printf("[PROCESS] %s\\n", filepath.Base(path))
		meta, err := validate.PrepareImage(path)
		if err != nil {
			fmt.Printf("  [SKIP] prep error: %v\\n", err)
			continue
		}
		if err := validate.SaveThumbnails(cacheDir, meta); err != nil {
			fmt.Printf("  [WARN] thumbs: %v\\n", err)
		}
		analysis, err := client.AnalyzeImage(meta.VLMBytes)
		if err != nil {
			fmt.Printf("  [FAIL] %v\\n", err)
			continue
		}
		fmt.Printf("  [OK] %s\\n", analysis.Caption)
		tags := ""
		if len(analysis.Tags) > 0 {
			tags = strings.Join(analysis.Tags, " ")
		}
		database.Exec(
			`INSERT INTO images (path, hash, caption, category, tags, has_text, has_faces, orientation, tier, processed_at, taken_at, phash)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			meta.Path, meta.Hash, analysis.Caption, analysis.Category, tags,
			boolToInt(analysis.HasText), boolToInt(analysis.HasFaces), analysis.Orientation,
			"scheduled", time.Now(), meta.TakenAt, meta.PHash,
		)
		if emb, err := embedClient.GetEmbedding(analysis.Caption); err == nil {
			blob := search.FloatsToBytes(emb)
			database.Exec("UPDATE images SET embedding = ? WHERE hash = ?", blob, meta.Hash)
		}
	}
	fmt.Println("[DONE] All images processed.")
}
