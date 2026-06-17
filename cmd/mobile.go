package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/scheduler"
	"fotoro/internal/validate"
)

// MobileServer handles mobile app communication
type MobileServer struct {
	db        *db.DB
	queue     *scheduler.JobQueue
	uploadsDir string
	cacheDir   string
}

func NewMobileServer(database *db.DB, queue *scheduler.JobQueue) *MobileServer {
	dbPath := os.Getenv("FOTORO_DB")
	if dbPath == "" {
		dbPath = "fotoro.db"
	}
	baseDir := filepath.Dir(dbPath)
	
	return &MobileServer{
		db:         database,
		queue:      queue,
		uploadsDir: filepath.Join(baseDir, "uploads"),
		cacheDir:   filepath.Join(baseDir, ".cache"),
	}
}

// RegisterRoutes adds mobile API endpoints to the mux
func (ms *MobileServer) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/mobile/upload", ms.handleMobileUpload)
	mux.HandleFunc("/api/mobile/status", ms.handleMobileStatus)
	mux.HandleFunc("/api/mobile/config", ms.handleMobileConfig)
	mux.HandleFunc("/api/mobile/gallery", ms.handleMobileGallery)
	mux.HandleFunc("/api/mobile/thumbnail/", ms.handleMobileThumbnail)
	mux.HandleFunc("/api/mobile/preview/", ms.handleMobilePreview)
	mux.HandleFunc("/api/mobile/delete/", ms.handleMobileDelete)
	mux.HandleFunc("/api/mobile/stats", ms.handleMobileStats)
	mux.HandleFunc("/api/mobile/queue", ms.handleMobileQueue)
	mux.HandleFunc("/api/mobile/pair", ms.handleMobilePair)
}

// handleMobileUpload receives images from the mobile app
func (ms *MobileServer) handleMobileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	
	// Parse multipart form (max 50MB)
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}
	
	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, `{"error":"no image field"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()
	
	// Get metadata from form
	originalName := r.FormValue("original_name")
	if originalName == "" {
		originalName = header.Filename
	}
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	
	// Create uploads directory if needed
	os.MkdirAll(ms.uploadsDir, 0755)
	
	// Save to staging area with timestamp prefix
	timestamp := time.Now().Format("20060102_150405")
	safeName := sanitizeFilename(originalName)
	stagingPath := filepath.Join(ms.uploadsDir, fmt.Sprintf("%s_%s", timestamp, safeName))
	
	f, err := os.Create(stagingPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"save failed: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	
	size, err := io.Copy(f, file)
	f.Close()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"write failed: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	
	// Add to queue
	upload, err := ms.queue.AddUpload(stagingPath, "mobile", originalName, contentType, size)
	if err != nil {
		// Remove file if queue add failed
		os.Remove(stagingPath)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusConflict)
		return
	}
	
	// Generate low-res preview immediately for offline viewing
	go ms.generateLowResPreview(stagingPath, upload.Hash)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"upload_id":     upload.ID,
		"hash":          upload.Hash,
		"status":        upload.Status,
		"size":          size,
		"queued_at":     upload.CreatedAt.Format(time.RFC3339),
		"message":       "Image queued for processing",
	})
}

// handleMobileStatus returns queue status for the mobile app
func (ms *MobileServer) handleMobileStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	
	stats, err := ms.queue.GetStats()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	
	// Add total library count
	var totalImages int
	ms.db.QueryRow("SELECT COUNT(*) FROM images").Scan(&totalImages)
	stats["total_images"] = totalImages
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleMobileConfig returns server configuration for mobile pairing
func (ms *MobileServer) handleMobileConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	
	// Get server info from environment or config
	tailscaleIP := os.Getenv("TAILSCALE_IP")
	if tailscaleIP == "" {
		// Try to get from tailscale status
		// This would need the tailscale module
		tailscaleIP = "not-configured"
	}
	
	serverName := os.Getenv("FOTORO_NODE_NAME")
	if serverName == "" {
		serverName = "fotoro-server"
	}
	
	config := map[string]interface{}{
		"server_name":    serverName,
		"tailscale_ip":   tailscaleIP,
		"api_version":    "v1",
		"max_upload_size": 50 * 1024 * 1024,
		"supported_formats": []string{"jpg", "jpeg", "png", "webp", "heic"},
		"features": map[string]bool{
			"offline_cache": true,
			"auto_upload":   true,
			"scheduled_processing": true,
		},
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(config)
}

// handleMobileGallery returns images grouped by date (like phone gallery)
func (ms *MobileServer) handleMobileGallery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	
	dateFilter := r.URL.Query().Get("date") // YYYY-MM-DD
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit := 100
	offset := (page - 1) * limit
	
	var query string
	var args []interface{}
	
	if dateFilter != "" {
		query = `
			SELECT path, hash, caption, category, 
			       strftime('%Y-%m-%d', COALESCE(taken_at, processed_at, created_at)) as date,
			       strftime('%H:%M', COALESCE(taken_at, processed_at, created_at)) as time
			FROM images 
			WHERE date = ?
			ORDER BY COALESCE(taken_at, processed_at, created_at) DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{dateFilter, limit, offset}
	} else {
		query = `
			SELECT path, hash, caption, category,
			       strftime('%Y-%m-%d', COALESCE(taken_at, processed_at, created_at)) as date,
			       strftime('%H:%M', COALESCE(taken_at, processed_at, created_at)) as time
			FROM images 
			ORDER BY COALESCE(taken_at, processed_at, created_at) DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{limit, offset}
	}
	
	rows, err := ms.db.Query(query, args...)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	type GalleryItem struct {
		Hash      string `json:"hash"`
		Caption   string `json:"caption"`
		Category  string `json:"category"`
		Date      string `json:"date"`
		Time      string `json:"time"`
		Thumbnail string `json:"thumbnail"`
		Preview   string `json:"preview"`
	}
	
	// Group by date
	gallery := make(map[string][]GalleryItem)
	var dates []string
	seenDates := make(map[string]bool)
	
	for rows.Next() {
		var item GalleryItem
		rows.Scan(&item.Hash, &item.Caption, &item.Category, &item.Date, &item.Time)
		item.Thumbnail = "/api/mobile/thumbnail/" + item.Hash + "?size=small"
		item.Preview = "/api/mobile/preview/" + item.Hash
		
		if !seenDates[item.Date] {
			seenDates[item.Date] = true
			dates = append(dates, item.Date)
		}
		gallery[item.Date] = append(gallery[item.Date], item)
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"dates":   dates,
		"gallery": gallery,
		"page":    page,
		"has_more": len(dates) == limit,
	})
}

// handleMobileThumbnail serves thumbnails to mobile
func (ms *MobileServer) handleMobileThumbnail(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/api/mobile/thumbnail/")
	size := r.URL.Query().Get("size")
	if size == "" {
		size = "small"
	}
	
	if len(hash) < 2 {
		http.Error(w, `{"error":"bad hash"}`, http.StatusBadRequest)
		return
	}
	
	path := filepath.Join(ms.cacheDir, "thumbnails", size, hash[:2], hash+".jpg")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Try to generate on-the-fly
		var imgPath string
		ms.db.QueryRow("SELECT path FROM images WHERE hash = ?", hash).Scan(&imgPath)
		if imgPath == "" {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		// Generate thumbnail
		meta, err := validate.PrepareImage(imgPath)
		if err != nil {
			http.Error(w, `{"error":"thumb generation failed"}`, http.StatusInternalServerError)
			return
		}
		validate.SaveThumbnails(ms.cacheDir, meta)
	}
	
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, path)
}

// handleMobilePreview serves low-res previews for offline cache
func (ms *MobileServer) handleMobilePreview(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/api/mobile/preview/")
	
	if len(hash) < 2 {
		http.Error(w, `{"error":"bad hash"}`, http.StatusBadRequest)
		return
	}
	
	// Check for low-res preview first
	previewPath := filepath.Join(ms.cacheDir, "previews", hash[:2], hash+"_preview.jpg")
	if _, err := os.Stat(previewPath); err == nil {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeFile(w, r, previewPath)
		return
	}
	
	// Fallback to medium thumbnail
	thumbPath := filepath.Join(ms.cacheDir, "thumbnails", "medium", hash[:2], hash+".jpg")
	if _, err := os.Stat(thumbPath); err == nil {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeFile(w, r, thumbPath)
		return
	}
	
	http.Error(w, `{"error":"preview not found"}`, http.StatusNotFound)
}

// handleMobileDelete removes an image
func (ms *MobileServer) handleMobileDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, `{"error":"DELETE only"}`, http.StatusMethodNotAllowed)
		return
	}
	
	hash := strings.TrimPrefix(r.URL.Path, "/api/mobile/delete/")
	
	// Get path first
	var path string
	ms.db.QueryRow("SELECT path FROM images WHERE hash = ?", hash).Scan(&path)
	
	// Delete from DB
	ms.db.Exec("DELETE FROM images WHERE hash = ?", hash)
	ms.db.Exec("DELETE FROM fts_captions WHERE rowid IN (SELECT id FROM images WHERE hash = ?)", hash)
	
	// Delete file if it exists in uploads dir
	if path != "" && strings.HasPrefix(path, ms.uploadsDir) {
		os.Remove(path)
	}
	
	// Delete thumbnails
	for _, size := range []string{"small", "medium"} {
		thumbPath := filepath.Join(ms.cacheDir, "thumbnails", size, hash[:2], hash+".jpg")
		os.Remove(thumbPath)
	}
	previewPath := filepath.Join(ms.cacheDir, "previews", hash[:2], hash+"_preview.jpg")
	os.Remove(previewPath)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// handleMobileStats returns comprehensive stats
func (ms *MobileServer) handleMobileStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	
	var stats struct {
		TotalImages    int            `json:"total_images"`
		TotalSize      int64          `json:"total_size_bytes"`
		Categories     map[string]int `json:"categories"`
		RecentUploads  int            `json:"recent_uploads_24h"`
		PendingQueue   int            `json:"pending_queue"`
		StorageUsed    int64          `json:"storage_used_bytes"`
	}
	
	ms.db.QueryRow("SELECT COUNT(*) FROM images").Scan(&stats.TotalImages)
	ms.db.QueryRow("SELECT COUNT(*) FROM images WHERE processed_at > datetime('now', '-1 day')").Scan(&stats.RecentUploads)
	ms.queue.GetPendingCount() // ignore error, will be 0
	
	// Calculate storage
	filepath.Walk(ms.uploadsDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			stats.TotalSize += info.Size()
		}
		return nil
	})
	
	// Categories
	stats.Categories = make(map[string]int)
	rows, _ := ms.db.Query("SELECT category, COUNT(*) FROM images GROUP BY category")
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var cat string
			var count int
			rows.Scan(&cat, &count)
			stats.Categories[cat] = count
		}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleMobileQueue returns detailed queue info
func (ms *MobileServer) handleMobileQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	
	uploads, err := ms.queue.GetPendingList(limit)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(uploads)
}

// handleMobilePair generates a pairing token for mobile app connection
func (ms *MobileServer) handleMobilePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	
	// Generate a pairing code
	pairingCode := generatePairingCode()
	
	// Store in DB with expiry
	expiry := time.Now().Add(5 * time.Minute)
	ms.db.Exec(`
		INSERT INTO mobile_pairing (code, created_at, expires_at, used) 
		VALUES (?, ?, ?, 0)
	`, pairingCode, time.Now(), expiry)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"pairing_code": pairingCode,
		"expires_at":   expiry.Format(time.RFC3339),
		"instructions": "Enter this code in your mobile app to pair",
	})
}

// generateLowResPreview creates a small preview for offline mobile viewing
// generateLowResPreview creates a small preview for offline mobile viewing
func (ms *MobileServer) generateLowResPreview(path, hash string) {
	previewDir := filepath.Join(ms.cacheDir, "previews", hash[:2])
	os.MkdirAll(previewDir, 0755)
	previewPath := filepath.Join(previewDir, hash+"_preview.jpg")

	// Skip if already exists
	if _, err := os.Stat(previewPath); err == nil {
		return
	}

	// Copy the small thumbnail as preview
	thumbPath := filepath.Join(ms.cacheDir, "thumbnails", "small", hash[:2], hash+".jpg")
	if data, err := os.ReadFile(thumbPath); err == nil {
		os.WriteFile(previewPath, data, 0644)
	}
}
func generatePairingCode() string {
	// Simple 6-digit code
	return fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
}

func sanitizeFilename(name string) string {
	// Remove path components and unsafe chars
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, "..", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	return name
}
