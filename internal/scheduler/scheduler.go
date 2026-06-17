package scheduler

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/ollama"
	"fotoro/internal/search"
	"fotoro/internal/validate"
)

// ScheduleConfig holds user preferences for processing
type ScheduleConfig struct {
	Enabled            bool
	ProcessTime        string // "02:00" for 2 AM
	ProcessDays        []int  // 0=Sunday, 1=Monday, etc. Empty = all days
	MaxConcurrent      int
	MaxPerBatch        int
	ProcessScreenshots bool
	ProcessDocuments   bool
	ProcessPhotos      bool
	ProcessVideos      bool
}

// JobQueue manages pending uploads and scheduled processing
type JobQueue struct {
	db         *db.DB
	config     ScheduleConfig
	ollama     *ollama.Client
	embed      *ollama.EmbedClient
	cacheDir   string
	uploadsDir string
	processing bool
	mu         sync.Mutex
	stopCh     chan struct{}
}

// PendingUpload represents an image waiting to be processed
type PendingUpload struct {
	ID           int
	Path         string
	Hash         string
	Source       string // "mobile", "web", "daemon"
	Status       string // "pending", "processing", "done", "failed", "duplicate"
	CreatedAt    time.Time
	ProcessedAt  *time.Time
	ErrorMsg     string
	Priority     int    // Higher = process first
	OriginalName string
	FileSize     int64
	ContentType  string
}

func NewJobQueue(database *db.DB, cfg ScheduleConfig) *JobQueue {
	dbPath := os.Getenv("FOTORO_DB")
	if dbPath == "" {
		dbPath = "fotoro.db"
	}

	return &JobQueue{
		db:         database,
		config:     cfg,
		cacheDir:   filepath.Join(filepath.Dir(dbPath), ".cache"),
		uploadsDir: filepath.Join(filepath.Dir(dbPath), "uploads"),
		stopCh:     make(chan struct{}),
	}
}

// InitDB creates the pending_uploads table
func (jq *JobQueue) InitDB() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pending_uploads (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL,
			hash TEXT,
			source TEXT DEFAULT 'mobile',
			status TEXT DEFAULT 'pending',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			processed_at DATETIME,
			error_msg TEXT,
			priority INTEGER DEFAULT 0,
			original_name TEXT,
			file_size INTEGER,
			content_type TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pending_status ON pending_uploads(status)`,
		`CREATE INDEX IF NOT EXISTS idx_pending_created ON pending_uploads(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_pending_hash ON pending_uploads(hash)`,
		`CREATE TABLE IF NOT EXISTS schedule_config (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			enabled INTEGER DEFAULT 0,
			process_time TEXT DEFAULT '02:00',
			process_days TEXT,
			max_concurrent INTEGER DEFAULT 2,
			max_per_batch INTEGER DEFAULT 100,
			process_screenshots INTEGER DEFAULT 1,
			process_documents INTEGER DEFAULT 1,
			process_photos INTEGER DEFAULT 1,
			process_videos INTEGER DEFAULT 0,
			last_run DATETIME,
			next_run DATETIME
		)`,
		`INSERT OR IGNORE INTO schedule_config (id) VALUES (1)`,
	}

	for _, stmt := range stmts {
		if _, err := jq.db.Exec(stmt); err != nil {
			return fmt.Errorf("init queue: %w", err)
		}
	}
	return nil
}

// LoadConfig reads schedule config from DB
func (jq *JobQueue) LoadConfig() error {
	var enabled, maxConc, maxBatch, procSS, procDoc, procPhoto, procVideo int
	var procTime, procDays string
	var lastRun, nextRun sql.NullTime

	err := jq.db.QueryRow(`
		SELECT enabled, process_time, process_days, max_concurrent, max_per_batch,
		       process_screenshots, process_documents, process_photos, process_videos,
		       last_run, next_run
		FROM schedule_config WHERE id = 1
	`).Scan(&enabled, &procTime, &procDays, &maxConc, &maxBatch,
		&procSS, &procDoc, &procPhoto, &procVideo, &lastRun, &nextRun)

	if err != nil {
		return err
	}

	jq.config.Enabled = enabled == 1
	jq.config.ProcessTime = procTime
	jq.config.MaxConcurrent = maxConc
	jq.config.MaxPerBatch = maxBatch
	jq.config.ProcessScreenshots = procSS == 1
	jq.config.ProcessDocuments = procDoc == 1
	jq.config.ProcessPhotos = procPhoto == 1
	jq.config.ProcessVideos = procVideo == 1

	if procDays != "" {
		for _, d := range strings.Split(procDays, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(d)); err == nil && n >= 0 && n <= 6 {
				jq.config.ProcessDays = append(jq.config.ProcessDays, n)
			}
		}
	}

	return nil
}

// SaveConfig writes schedule config to DB
func (jq *JobQueue) SaveConfig(cfg ScheduleConfig) error {
	days := ""
	if len(cfg.ProcessDays) > 0 {
		parts := make([]string, len(cfg.ProcessDays))
		for i, d := range cfg.ProcessDays {
			parts[i] = strconv.Itoa(d)
		}
		days = strings.Join(parts, ",")
	}

	_, err := jq.db.Exec(`
		UPDATE schedule_config SET
			enabled = ?,
			process_time = ?,
			process_days = ?,
			max_concurrent = ?,
			max_per_batch = ?,
			process_screenshots = ?,
			process_documents = ?,
			process_photos = ?,
			process_videos = ?
		WHERE id = 1
	`, boolToInt(cfg.Enabled), cfg.ProcessTime, days, cfg.MaxConcurrent, cfg.MaxPerBatch,
		boolToInt(cfg.ProcessScreenshots), boolToInt(cfg.ProcessDocuments),
		boolToInt(cfg.ProcessPhotos), boolToInt(cfg.ProcessVideos))

	if err != nil {
		return err
	}

	jq.config = cfg
	jq.recalculateNextRun()
	return nil
}

// recalculateNextRun computes the next scheduled run time
func (jq *JobQueue) recalculateNextRun() {
	if !jq.config.Enabled {
		jq.db.Exec("UPDATE schedule_config SET next_run = NULL WHERE id = 1")
		return
	}

	parts := strings.Split(jq.config.ProcessTime, ":")
	if len(parts) != 2 {
		return
	}
	hour, _ := strconv.Atoi(parts[0])
	min, _ := strconv.Atoi(parts[1])

	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())

	if next.Before(now) {
		next = next.Add(24 * time.Hour)
	}

	// Check if day is allowed
	if len(jq.config.ProcessDays) > 0 {
		for {
			dayAllowed := false
			dayNum := int(next.Weekday())
			for _, d := range jq.config.ProcessDays {
				if d == dayNum {
					dayAllowed = true
					break
				}
			}
			if dayAllowed {
				break
			}
			next = next.Add(24 * time.Hour)
		}
	}

	jq.db.Exec("UPDATE schedule_config SET next_run = ? WHERE id = 1", next)
}

// GetNextRun returns the next scheduled processing time
func (jq *JobQueue) GetNextRun() (*time.Time, error) {
	var nextRun sql.NullTime
	err := jq.db.QueryRow("SELECT next_run FROM schedule_config WHERE id = 1").Scan(&nextRun)
	if err != nil {
		return nil, err
	}
	if !nextRun.Valid {
		return nil, nil
	}
	return &nextRun.Time, nil
}

// AddUpload adds a new image to the pending queue
func (jq *JobQueue) AddUpload(path, source, originalName, contentType string, fileSize int64) (*PendingUpload, error) {
	// Compute hash to check duplicates
	hash, err := validate.FastHash(path)
	if err != nil {
		return nil, fmt.Errorf("hash failed: %w", err)
	}

	// Check if already in main DB
	var existing int
	jq.db.QueryRow("SELECT 1 FROM images WHERE hash = ?", hash).Scan(&existing)
	if existing == 1 {
		// Still add to queue but mark as duplicate
		res, err := jq.db.Exec(`
			INSERT INTO pending_uploads (path, hash, source, status, original_name, file_size, content_type)
			VALUES (?, ?, ?, 'duplicate', ?, ?, ?)
		`, path, hash, source, originalName, fileSize, contentType)
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		return &PendingUpload{ID: int(id), Hash: hash, Status: "duplicate"}, nil
	}

	// Check if already pending
	var pendingID int
	jq.db.QueryRow("SELECT id FROM pending_uploads WHERE hash = ? AND status = 'pending'", hash).Scan(&pendingID)
	if pendingID > 0 {
		return nil, fmt.Errorf("already pending: upload #%d", pendingID)
	}

	res, err := jq.db.Exec(`
		INSERT INTO pending_uploads (path, hash, source, status, original_name, file_size, content_type)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)
	`, path, hash, source, originalName, fileSize, contentType)
	if err != nil {
		return nil, err
	}

	id, _ := res.LastInsertId()
	return &PendingUpload{
		ID:           int(id),
		Path:         path,
		Hash:         hash,
		Source:       source,
		Status:       "pending",
		CreatedAt:    time.Now(),
		OriginalName: originalName,
		FileSize:     fileSize,
		ContentType:  contentType,
	}, nil
}

// GetPendingCount returns number of pending uploads
func (jq *JobQueue) GetPendingCount() (int, error) {
	var count int
	err := jq.db.QueryRow("SELECT COUNT(*) FROM pending_uploads WHERE status = 'pending'").Scan(&count)
	return count, err
}

// GetPendingList returns pending uploads
func (jq *JobQueue) GetPendingList(limit int) ([]PendingUpload, error) {
	rows, err := jq.db.Query(`
		SELECT id, path, hash, source, status, created_at, processed_at, error_msg, priority, original_name, file_size, content_type
		FROM pending_uploads WHERE status = 'pending' ORDER BY priority DESC, created_at ASC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var uploads []PendingUpload
	for rows.Next() {
		var u PendingUpload
		var procAt sql.NullTime
		rows.Scan(&u.ID, &u.Path, &u.Hash, &u.Source, &u.Status, &u.CreatedAt, &procAt, &u.ErrorMsg, &u.Priority, &u.OriginalName, &u.FileSize, &u.ContentType)
		if procAt.Valid {
			u.ProcessedAt = &procAt.Time
		}
		uploads = append(uploads, u)
	}
	return uploads, nil
}

// ProcessNow immediately processes all pending uploads
func (jq *JobQueue) ProcessNow(model string) error {
	jq.mu.Lock()
	if jq.processing {
		jq.mu.Unlock()
		return fmt.Errorf("already processing")
	}
	jq.processing = true
	jq.mu.Unlock()

	defer func() {
		jq.mu.Lock()
		jq.processing = false
		jq.mu.Unlock()
		jq.db.Exec("UPDATE schedule_config SET last_run = ? WHERE id = 1", time.Now())
	}()

	if jq.ollama == nil {
		jq.ollama = ollama.NewClient("", model)
	}
	if jq.embed == nil {
		jq.embed = ollama.NewEmbedClient()
	}

	uploads, err := jq.GetPendingList(jq.config.MaxPerBatch)
	if err != nil {
		return err
	}
	if len(uploads) == 0 {
		return nil
	}

	fmt.Printf("[SCHEDULER] Processing %d pending uploads...\\n", len(uploads))

	var wg sync.WaitGroup
	sem := make(chan struct{}, jq.config.MaxConcurrent)

	for _, upload := range uploads {
		wg.Add(1)
		sem <- struct{}{}
		go func(u PendingUpload) {
			defer wg.Done()
			defer func() { <-sem }()
			jq.processUpload(u)
		}(upload)
	}

	wg.Wait()
	fmt.Println("[SCHEDULER] Batch processing complete")
	return nil
}

func (jq *JobQueue) processUpload(u PendingUpload) {
	// Mark as processing
	jq.db.Exec("UPDATE pending_uploads SET status = 'processing' WHERE id = ?", u.ID)

	// Validate and prepare image
	meta, err := validate.PrepareImage(u.Path)
	if err != nil {
		jq.db.Exec("UPDATE pending_uploads SET status = 'failed', error_msg = ? WHERE id = ?", err.Error(), u.ID)
		return
	}

	// Check perceptual duplicate
	if meta.PHash != "" {
		var dupHash string
		jq.db.QueryRow("SELECT hash FROM images WHERE phash = ?", meta.PHash).Scan(&dupHash)
		if dupHash != "" {
			jq.db.Exec("UPDATE pending_uploads SET status = 'duplicate', error_msg = ? WHERE id = ?", "perceptual duplicate", u.ID)
			return
		}
	}

	// Save thumbnails
	if err := validate.SaveThumbnails(jq.cacheDir, meta); err != nil {
		fmt.Printf("[WARN] thumbs failed for %s: %v\\n", u.Hash[:8], err)
	}

	// VLM analysis
	analysis, err := jq.ollama.AnalyzeImage(meta.VLMBytes)
	if err != nil {
		jq.db.Exec("UPDATE pending_uploads SET status = 'failed', error_msg = ? WHERE id = ?", err.Error(), u.ID)
		jq.db.Exec(`
			INSERT OR IGNORE INTO images (path, hash, caption, category, tier, taken_at, phash)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, meta.Path, meta.Hash, "", "failed", "scheduled", meta.TakenAt, meta.PHash)
		return
	}

	// Insert into main DB
	tags := ""
	if len(analysis.Tags) > 0 {
		tags = strings.Join(analysis.Tags, " ")
	}
	_, err = jq.db.Exec(`
		INSERT INTO images (path, hash, caption, category, tags, has_text, has_faces, orientation, tier, processed_at, taken_at, phash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, meta.Path, meta.Hash, analysis.Caption, analysis.Category, tags,
		boolToInt(analysis.HasText), boolToInt(analysis.HasFaces), analysis.Orientation,
		"scheduled", time.Now(), meta.TakenAt, meta.PHash)

	if err != nil {
		jq.db.Exec("UPDATE pending_uploads SET status = 'failed', error_msg = ? WHERE id = ?", err.Error(), u.ID)
		return
	}

	// Generate embedding
	if emb, err := jq.embed.GetEmbedding(analysis.Caption); err == nil {
		blob := search.FloatsToBytes(emb)
		jq.db.Exec("UPDATE images SET embedding = ? WHERE hash = ?", blob, meta.Hash)
	}

	// Mark as done
	jq.db.Exec("UPDATE pending_uploads SET status = 'done', processed_at = ? WHERE id = ?", time.Now(), u.ID)
	fmt.Printf("[SCHEDULER] Done: %s | %s\\n", u.Hash[:8], analysis.Category)
}

// StartDaemon begins the background scheduler
func (jq *JobQueue) StartDaemon(model string) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if !jq.config.Enabled {
					continue
				}

				nextRun, err := jq.GetNextRun()
				if err != nil || nextRun == nil {
					continue
				}

				now := time.Now()
				if now.After(*nextRun) || now.Equal(*nextRun) {
					count, _ := jq.GetPendingCount()
					if count > 0 {
						fmt.Printf("[SCHEDULER] Triggered at %s — %d items pending\\n", now.Format("15:04"), count)
						jq.ProcessNow(model)
						jq.recalculateNextRun()
					}
				}
			case <-jq.stopCh:
				return
			}
		}
	}()
	fmt.Println("[SCHEDULER] Background daemon started")
}

// Stop stops the background scheduler
func (jq *JobQueue) Stop() {
	close(jq.stopCh)
}

// IsProcessing returns true if currently processing
func (jq *JobQueue) IsProcessing() bool {
	jq.mu.Lock()
	defer jq.mu.Unlock()
	return jq.processing
}

// GetStats returns queue statistics
func (jq *JobQueue) GetStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	var counts struct {
		Pending   int
		Done      int
		Failed    int
		Duplicate int
		Total     int
	}

	jq.db.QueryRow("SELECT COUNT(*) FROM pending_uploads WHERE status = 'pending'").Scan(&counts.Pending)
	jq.db.QueryRow("SELECT COUNT(*) FROM pending_uploads WHERE status = 'done'").Scan(&counts.Done)
	jq.db.QueryRow("SELECT COUNT(*) FROM pending_uploads WHERE status = 'failed'").Scan(&counts.Failed)
	jq.db.QueryRow("SELECT COUNT(*) FROM pending_uploads WHERE status = 'duplicate'").Scan(&counts.Duplicate)
	jq.db.QueryRow("SELECT COUNT(*) FROM pending_uploads").Scan(&counts.Total)

	stats["pending"] = counts.Pending
	stats["done"] = counts.Done
	stats["failed"] = counts.Failed
	stats["duplicate"] = counts.Duplicate
	stats["total"] = counts.Total
	stats["processing"] = jq.IsProcessing()
	stats["enabled"] = jq.config.Enabled
	stats["process_time"] = jq.config.ProcessTime
	stats["max_per_batch"] = jq.config.MaxPerBatch

	nextRun, _ := jq.GetNextRun()
	if nextRun != nil {
		stats["next_run"] = nextRun.Format(time.RFC3339)
	}

	return stats, nil
}

// CleanupOld removes completed uploads older than N days
func (jq *JobQueue) CleanupOld(days int) error {
	cutoff := time.Now().AddDate(0, 0, -days)
	_, err := jq.db.Exec("DELETE FROM pending_uploads WHERE status IN ('done', 'duplicate') AND processed_at < ?", cutoff)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
