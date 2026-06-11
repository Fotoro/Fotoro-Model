package cmd

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/ollama"
	"fotoro/internal/search"
)

func RunBackfill(dbPath, model string) {
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	client := ollama.NewEmbedClient()
	if err := client.HealthCheck(); err != nil {
		fmt.Printf("Embed server not running. Start it with:\n")
		fmt.Printf("  ./llama-server -m nomic-embed-text-v1.5.f16.gguf --host 127.0.0.1:8082 --embeddings\n")
		fmt.Printf("Then: EMBED_ADDR=http://127.0.0.1:8082 ./fotoro backfill\n")
		os.Exit(1)
	}

	// Count total to backfill
	var total int
	database.QueryRow("SELECT COUNT(*) FROM images WHERE embedding IS NULL").Scan(&total)
	fmt.Printf("[INIT] %d images need embeddings\n", total)

	// Batch fetch for efficiency
	batchSize := 100
	offset := 0
	count := int64(0)
	start := time.Now()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // 4 concurrent embed requests

	for offset < total {
		rows, err := database.Query(
			"SELECT id, caption, hash FROM images WHERE embedding IS NULL LIMIT ? OFFSET ?",
			batchSize, offset,
		)
		if err != nil {
			panic(err)
		}

		var batch []struct {
			id     int
			caption string
			hash   string
		}
		for rows.Next() {
			var id int
			var caption, hash string
			rows.Scan(&id, &caption, &hash)
			batch = append(batch, struct {
				id      int
				caption string
				hash    string
			}{id, caption, hash})
		}
		rows.Close()

		if len(batch) == 0 {
			break
		}

		for _, item := range batch {
			wg.Add(1)
			sem <- struct{}{}
			go func(id int, caption, hash string) {
				defer wg.Done()
				defer func() { <-sem }()

				text := caption
				if text == "" {
					text = "image"
				}

				emb, err := client.GetEmbedding(text)
				if err != nil {
					fmt.Printf("[SKIP] %s: %v\n", hash, err)
					return
				}

				blob := search.FloatsToBytes(emb)
				_, err = database.Exec("UPDATE images SET embedding = ? WHERE id = ?", blob, id)
				if err != nil {
					fmt.Printf("[FAIL] %s: DB %v\n", hash, err)
					return
				}

				c := atomic.AddInt64(&count, 1)
				if c%10 == 0 {
					fmt.Printf("[OK] %d embeddings done... (%.1f sec)\n", c, time.Since(start).Seconds())
				}
			}(item.id, item.caption, item.hash)
		}

		offset += batchSize
	}

	wg.Wait()
	fmt.Printf("\nDone. Backfilled %d embeddings in %.1f minutes\n", count, time.Since(start).Minutes())
}