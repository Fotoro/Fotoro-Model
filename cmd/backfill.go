package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/ollama"
	"fotoro/internal/search"
)

// embedText composes the document text for an image row. Returns false when
// there is nothing meaningful to embed.
func embedText(caption, tags, category string) (string, bool) {
	parts := make([]string, 0, 3)
	for _, s := range []string{caption, tags, category} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, ". "), true
}

// BackfillEmbeddings embeds up to limit image rows whose stored vector is
// missing or was produced by a different model/dimension. Returns the number
// embedded and the number skipped for lack of text.
func BackfillEmbeddings(database *sql.DB, client *ollama.EmbedClient, limit int) (done, skipped int, err error) {
	rows, err := database.Query(`
		SELECT id, COALESCE(caption,''), COALESCE(tags,''), COALESCE(category,''), hash
		FROM images
		WHERE (embed_model IS NULL OR embed_model != ? OR embed_dim != ?)
		ORDER BY id LIMIT ?`, client.Model(), client.Dim(), limit)
	if err != nil {
		return 0, 0, err
	}
	type item struct {
		id             int
		caption, tags  string
		category, hash string
	}
	var batch []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.caption, &it.tags, &it.category, &it.hash); err != nil {
			rows.Close()
			return done, skipped, err
		}
		batch = append(batch, it)
	}
	rows.Close()

	for _, it := range batch {
		text, ok := embedText(it.caption, it.tags, it.category)
		if !ok {
			// Mark as embedded with the current model so it is not rescanned
			// every pass; a future caption update will re-trigger via
			// embedded_at only when text exists. Store a NULL embedding.
			database.Exec(`UPDATE images SET embed_model = ?, embed_dim = ?, embedded_at = ? WHERE id = ?`,
				client.Model(), client.Dim(), time.Now().UTC(), it.id)
			skipped++
			continue
		}
		emb, err := client.EmbedDocument(text)
		if err != nil {
			return done, skipped, fmt.Errorf("embed %s: %w", it.hash[:12], err)
		}
		if _, err := database.Exec(`UPDATE images SET embedding = ?, embed_model = ?, embed_dim = ?, embedded_at = ? WHERE id = ?`,
			search.FloatsToBytes(emb), client.Model(), len(emb), time.Now().UTC(), it.id); err != nil {
			return done, skipped, err
		}
		done++
	}
	return done, skipped, nil
}

// EmbedPending counts rows still needing an embedding for the current model.
func EmbedPending(database *sql.DB, client *ollama.EmbedClient) (int, error) {
	var n int
	err := database.QueryRow(`
		SELECT COUNT(*) FROM images
		WHERE (embed_model IS NULL OR embed_model != ? OR embed_dim != ?)`,
		client.Model(), client.Dim()).Scan(&n)
	return n, err
}

func RunBackfill(dbPath, model string) {
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	client := ollama.NewEmbedClient()
	if err := client.HealthCheck(); err != nil {
		fmt.Printf("Ollama not reachable. Start it and pull the model:\n")
		fmt.Printf("  ollama pull embeddinggemma\n")
		fmt.Printf("Then: ./fotoro backfill\n")
		os.Exit(1)
	}
	fmt.Printf("[INIT] embed model=%s dim=%d\n", client.Model(), client.Dim())

	pending, _ := EmbedPending(database.DB, client)
	fmt.Printf("[INIT] %d images need embeddings\n", pending)

	count := int64(0)
	start := time.Now()
	for {
		done, skipped, err := BackfillEmbeddings(database.DB, client, 200)
		if err != nil {
			fmt.Printf("[FAIL] %v\n", err)
			break
		}
		atomic.AddInt64(&count, int64(done))
		if done == 0 && skipped == 0 {
			break
		}
		fmt.Printf("[OK] %d embeddings, %d skipped... (%.1f sec)\n",
			atomic.LoadInt64(&count), skipped, time.Since(start).Seconds())
		if done+skipped < 200 {
			break
		}
	}
	fmt.Printf("\nDone. Backfilled %d embeddings in %.1f minutes\n", count, time.Since(start).Minutes())
}
