package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fotoro/internal/api"
	"fotoro/internal/validate"

	_ "modernc.org/sqlite"
)

func main() {
	watchDir := os.Getenv("FOTORO_WATCH_DIR")
	if watchDir == "" {
		watchDir = "/home/daredevil/Downloads/FotoroModel/images"
	}

	db, _ := sql.Open("sqlite", "fotoro.db")
	defer db.Close()
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")

	// Schema with content_type
	db.Exec(`CREATE TABLE IF NOT EXISTS images (
		id INTEGER PRIMARY KEY, path TEXT NOT NULL, hash TEXT UNIQUE NOT NULL,
		caption TEXT NOT NULL, content_type TEXT DEFAULT 'unknown',
		tier TEXT DEFAULT 'bulk', created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS fts_captions USING fts5(path, caption)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_hash ON images(hash)`)

	fmt.Println("Fotoro daemon started.")
	fmt.Println("Watching:", watchDir)
	fmt.Println("Logs: tail -f /tmp/fotoro-daemon.log")

	f, _ := os.OpenFile("/tmp/fotoro-daemon.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	defer f.Close()

	log := func(format string, args ...interface{}) {
		line := fmt.Sprintf(format, args...)
		fmt.Println(line)
		f.WriteString(line + "\n")
		f.Sync()
	}

	processedTotal := 0

	for {
		var files []string
		filepath.WalkDir(watchDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" {
				return nil
			}

			h := quickHash(path)
			var exists int
			db.QueryRow("SELECT 1 FROM images WHERE hash = ?", h).Scan(&exists)
			if exists != 1 {
				files = append(files, path)
			}
			return nil
		})

		if len(files) == 0 {
			log("[DAEMON] No new images. Sleeping 60s...")
			time.Sleep(60 * time.Second)
			continue
		}

		log("[DAEMON] Found %d new images to process", len(files))

		for _, path := range files {
			img, err := validate.ProcessImage(path)
			if err != nil {
				log("[SKIP] %s: %v", filepath.Base(path), err)
				continue
			}

			caption, err := api.CaptionImage(img.JPEGBytes)
			if err != nil {
				log("[FAIL] %s: %v", filepath.Base(path), err)
				continue
			}

			// Parse content type from the new prompt format
			contentType, cleanCaption := parseContentType(caption)

			db.Exec("INSERT INTO images (path, hash, caption, content_type, tier) VALUES (?, ?, ?, ?, ?)",
				img.Path, img.Hash, cleanCaption, contentType, "bulk")
			db.Exec("INSERT INTO fts_captions (path, caption) VALUES (?, ?)",
				img.Path, cleanCaption)

			processedTotal++
			preview := cleanCaption
			if len(preview) > 50 {
				preview = preview[:50]
			}
			log("[OK] %d total | %s | %s | %s", processedTotal, filepath.Base(path), contentType, preview)
		}

		log("[DAEMON] Batch complete. %d total processed. Sleeping 60s...", processedTotal)
		time.Sleep(60 * time.Second)
	}
}

func parseContentType(caption string) (string, string) {
	// Your prompt forces: SCREENSHOT: or PHOTO: or DOCUMENT: or NOTE:
	lower := strings.ToLower(caption)
	
	if strings.HasPrefix(lower, "screenshot:") {
		return "screenshot", strings.TrimSpace(caption[11:])
	}
	if strings.HasPrefix(lower, "photo:") {
		return "photo", strings.TrimSpace(caption[6:])
	}
	if strings.HasPrefix(lower, "document:") {
		return "document", strings.TrimSpace(caption[9:])
	}
	if strings.HasPrefix(lower, "note:") {
		return "note", strings.TrimSpace(caption[5:])
	}
	
	return "unknown", caption
}

func quickHash(path string) string {
	f, _ := os.Open(path)
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	return fmt.Sprintf("%x", buf[:n])
}
