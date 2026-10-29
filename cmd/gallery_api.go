package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fotoro/internal/auth"
	"fotoro/internal/localtoken"
)

func registerGalleryRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("/status", s.withCORS(s.withStrictAuth(s.handleGalleryStatus)))
	mux.HandleFunc("/stats", s.withCORS(s.withStrictAuth(s.handleGalleryLibraryStats)))
	mux.HandleFunc("/devices", s.withCORS(s.withStrictAuth(s.handleGalleryDevices)))
	mux.HandleFunc("/photos", s.withCORS(s.withStrictAuth(s.handleGalleryPhotos)))
	mux.HandleFunc("/photo/", s.withCORS(s.withStrictAuth(s.handleGalleryPhoto)))
	mux.HandleFunc("/thumb/", s.withCORS(s.withStrictAuth(s.handleGalleryThumb)))
	mux.HandleFunc("/albums", s.withCORS(s.withStrictAuth(s.handleGalleryAlbums)))
	mux.HandleFunc("/search", s.withCORS(s.withStrictAuth(s.handleGallerySearch)))
}

func (s *Server) withCORS(next http.HandlerFunc) http.HandlerFunc {
	allowed := strings.TrimSpace(os.Getenv("FOTORO_CORS_ORIGINS"))
	if allowed == "" {
		allowed = "https://fotoro.vercel.app,https://app.fotoro.com,http://localhost:3000"
	}
	allowedList := strings.Split(allowed, ",")

	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowOrigin := ""
		for _, o := range allowedList {
			o = strings.TrimSpace(o)
			if o == origin || o == "*" {
				allowOrigin = origin
				if o == "*" && origin == "" {
					allowOrigin = "*"
				}
				break
			}
			// Allow any *.vercel.app preview deployment
			if strings.HasSuffix(origin, ".vercel.app") && strings.Contains(o, "vercel.app") {
				allowOrigin = origin
				break
			}
		}
		if allowOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Upload-Offset, Range, If-Range, If-None-Match, If-Modified-Since")
			w.Header().Set("Access-Control-Expose-Headers", "Upload-Offset, Upload-Length, Location, Accept-Ranges, Content-Range, ETag, Last-Modified")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func (s *Server) withStrictAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, `{"error":"invalid authorization header"}`, http.StatusUnauthorized)
			return
		}
		user, err := s.verifyBearerToken(parts[1])
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusUnauthorized)
			return
		}
		if err := s.authorizeUser(r, user); err != nil {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next(w, r.WithContext(auth.WithUser(r.Context(), user)))
	}
}

func (s *Server) verifyBearerToken(token string) (*auth.User, error) {
	if strings.HasPrefix(token, deviceTokenPrefix) {
		return s.verifyDeviceToken(token)
	}
	if claims, err := localtoken.Verify(token); err == nil && claims.UserID != "" {
		return &auth.User{ID: claims.UserID, Provider: "fotoro-local"}, nil
	}
	if s.supabaseAuth != nil {
		return s.supabaseAuth.VerifyToken(token)
	}
	return nil, fmt.Errorf("auth not configured")
}

func (s *Server) handleGalleryStatus(w http.ResponseWriter, r *http.Request) {
	var total int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM images").Scan(&total)

	state := "online"
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    state,
		"photos":    total,
		"server":    "fotoro",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

type galleryPhoto struct {
	ID        string `json:"id"`
	Caption   string `json:"caption"`
	Category  string `json:"category"`
	TakenAt   string `json:"taken_at,omitempty"`
	Thumbnail string `json:"thumbnail"`
}

func (s *Server) handleGalleryPhotos(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 200 {
		limit = 60
	}
	offset := (page - 1) * limit

	var total int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM images").Scan(&total)

	sortParam := r.URL.Query().Get("sort")
	orderBy := "COALESCE(processed_at, created_at) DESC, id DESC"
	if sortParam == "date" || sortParam == "taken" || sortParam == "captured" {
		orderBy = "COALESCE(taken_at, processed_at, created_at) DESC, id DESC"
	}

	rows, err := s.db.Query(`
		SELECT hash, caption, category,
		       COALESCE(taken_at, processed_at, created_at) as taken
		FROM images ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	photos := make([]galleryPhoto, 0, limit)
	for rows.Next() {
		var p galleryPhoto
		var taken time.Time
		if err := rows.Scan(&p.ID, &p.Caption, &p.Category, &taken); err != nil {
			continue
		}
		if len(p.ID) < 2 {
			continue
		}
		if !taken.IsZero() {
			p.TakenAt = taken.UTC().Format(time.RFC3339)
		}
		p.Thumbnail = "/thumb/" + p.ID + "?size=medium"
		photos = append(photos, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"photos": photos,
		"page":   page,
		"limit":  limit,
		"count":  len(photos),
		"total":  total,
	})
}

func (s *Server) handleGalleryPhoto(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/photo/")
	if id == "" {
		http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
		return
	}
	var path string
	if err := s.db.QueryRow("SELECT path FROM images WHERE hash = ?", id).Scan(&path); err != nil || path == "" {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}

func (s *Server) handleGalleryThumb(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/thumb/")
	if len(id) < 2 {
		http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
		return
	}
	size := r.URL.Query().Get("size")
	if size == "" {
		size = "medium"
	}
	path := filepath.Join(s.cacheDir, "thumbnails", size, id[:2], id+".jpg")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

func (s *Server) handleGalleryAlbums(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`
		SELECT a.id, a.name, a.description, COUNT(ai.hash) as photo_count
		FROM albums a
		LEFT JOIN album_images ai ON ai.album_id = a.id
		GROUP BY a.id ORDER BY a.name`)
	if err != nil {
		// albums table may be empty on fresh installs
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"albums": []interface{}{}})
		return
	}
	defer rows.Close()

	type album struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		PhotoCount  int    `json:"photo_count"`
	}
	var albums []album
	for rows.Next() {
		var a album
		var desc *string
		rows.Scan(&a.ID, &a.Name, &desc, &a.PhotoCount)
		if desc != nil {
			a.Description = *desc
		}
		albums = append(albums, a)
	}
	if albums == nil {
		albums = []album{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"albums": albums})
}

func (s *Server) handleGallerySearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 50

	if r.Method == http.MethodPost {
		var body struct {
			Q     string `json:"q"`
			Limit int    `json:"limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if body.Q != "" {
				q = body.Q
			}
			if body.Limit > 0 && body.Limit <= 200 {
				limit = body.Limit
			}
		}
	}

	// Reuse existing search logic via internal redirect
	r.URL.RawQuery = fmt.Sprintf("q=%s", q)
	w.Header().Set("Content-Type", "application/json")

	type resultItem struct {
		ID        string  `json:"id"`
		Caption   string  `json:"caption"`
		Category  string  `json:"category"`
		Score     float64 `json:"score,omitempty"`
		Thumbnail string  `json:"thumbnail"`
	}

	// Delegate to handleSearch internals — call handleSearch and transform?
	// Simpler: duplicate minimal FTS path
	if q == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"query": "", "results": []resultItem{}})
		return
	}

	rec := &responseRecorder{header: make(http.Header)}
	s.handleSearch(rec, r)
	if rec.status != 0 && rec.status != http.StatusOK {
		http.Error(w, rec.body.String(), rec.status)
		return
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(rec.body.Bytes(), &raw); err != nil {
		http.Error(w, `{"error":"search failed"}`, http.StatusInternalServerError)
		return
	}
	resultsRaw, _ := raw["results"].([]interface{})
	out := make([]resultItem, 0, len(resultsRaw))
	for _, item := range resultsRaw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		hash, _ := m["hash"].(string)
		out = append(out, resultItem{
			ID:        hash,
			Caption:   str(m["caption"]),
			Category:  str(m["category"]),
			Score:     num(m["score"]),
			Thumbnail: "/thumb/" + hash + "?size=medium",
		})
		if len(out) >= limit {
			break
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"query":   q,
		"count":   len(out),
		"results": out,
	})
}

func str(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func num(v interface{}) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func (s *Server) handleGalleryLibraryStats(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.dashboardMetrics(r.Context())
	if err != nil {
		http.Error(w, `{"error":"metrics unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"photos_total":       metrics.Total,
		"photos_processed":   metrics.Processed,
		"photos_failed":      metrics.Failed,
		"thumbnails_medium":  metrics.ThumbnailsMedium,
		"storage_used_bytes": metrics.StorageUsedBytes,
		"disk_total_bytes":   metrics.DiskTotalBytes,
		"disk_free_bytes":    metrics.DiskFreeBytes,
		"devices_count":      metrics.DevicesCount,
		"photos_with_faces":  metrics.PhotosWithFaces,
		"queue_pending":      metrics.QueuePending,
		"queue_failed":       metrics.QueueFailed,
		"search_vectors":     metrics.SearchVectors,
		"backup_verified":    metrics.BackupVerified,
		"backup_pending":     metrics.BackupPending,
		"collected_at":       metrics.CollectedAt,
	})
}

func (s *Server) handleGalleryDevices(w http.ResponseWriter, r *http.Request) {
	type device struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Platform string `json:"platform"`
		Status   string `json:"status"`
		LastSeen string `json:"last_seen,omitempty"`
		Items    string `json:"items"`
	}

	devices := make([]device, 0)

	cfg, _ := s.db.GetServerConfig()
	serverName := "fotoro-server"
	if cfg != nil {
		if n, ok := cfg["server_name"].(string); ok && n != "" {
			serverName = n
		}
	}
	var photoCount int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM images").Scan(&photoCount)
	devices = append(devices, device{
		ID:       "local-server",
		Name:     serverName,
		Platform: "server",
		Status:   "active",
		Items:    fmt.Sprintf("%d photos", photoCount),
	})

	rows, err := s.db.Query(`
		SELECT d.device_id, d.device_name, d.platform, d.last_seen, d.is_active,
			COALESCE(a.items, 0)
		FROM mobile_devices d
		LEFT JOIN (
			SELECT device_id, COUNT(DISTINCT media_asset_id) AS items
			FROM device_assets WHERE media_asset_id IS NOT NULL GROUP BY device_id
		) a ON a.device_id = d.device_id
		ORDER BY d.last_seen DESC`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d device
			var lastSeen sql.NullTime
			var active int
			var platform sql.NullString
			var items int
			if err := rows.Scan(&d.ID, &d.Name, &platform, &lastSeen, &active, &items); err != nil {
				continue
			}
			if platform.Valid {
				d.Platform = platform.String
			}
			if active == 1 && lastSeen.Valid && time.Since(lastSeen.Time) < 5*time.Minute {
				d.Status = "active"
			} else {
				d.Status = "idle"
			}
			if lastSeen.Valid {
				d.LastSeen = lastSeen.Time.UTC().Format(time.RFC3339)
			}
			unit := "items"
			if items == 1 {
				unit = "item"
			}
			d.Items = fmt.Sprintf("%d %s", items, unit)
			devices = append(devices, d)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"devices": devices})
}

func (s *Server) dbPath() string {
	if p := os.Getenv("FOTORO_DB"); p != "" {
		return p
	}
	return "fotoro.db"
}

type responseRecorder struct {
	status int
	body   bytes.Buffer
	header http.Header
}

func (r *responseRecorder) Header() http.Header         { return r.header }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *responseRecorder) WriteHeader(code int)        { r.status = code }
