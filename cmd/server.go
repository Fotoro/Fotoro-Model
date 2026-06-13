package cmd

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
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

type Server struct {
	db       *db.DB
	cacheDir string
	ollama   *ollama.Client
	embed    *ollama.EmbedClient
	index    *search.Index
}

// snapCache holds recent image-query embeddings so repeated "similar" requests
// don't re-run the VLM.
var snapCache sync.Map // sha256[:8] hex → []float32

func RunServer(addr, dbPath, model string) {
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	idx := search.NewIndex()
	if err := idx.LoadFromDB(database.DB); err != nil {
		fmt.Printf("[WARN] Could not load vectors: %v\n", err)
	} else {
		fmt.Printf("[INIT] Loaded vectors into memory\n")
	}

	s := &Server{
		db:       database,
		cacheDir: filepath.Join(filepath.Dir(dbPath), ".cache"),
		ollama:   ollama.NewClient("", model),
		embed:    ollama.NewEmbedClient(),
		index:    idx,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/similar", s.handleSimilar)
	mux.HandleFunc("/api/images", s.handleList)
	mux.HandleFunc("/api/image/", s.handleImage)
	mux.HandleFunc("/api/thumbnail/", s.handleThumbnail)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/ingest", s.handleIngest)
	mux.HandleFunc("/api/stats", s.handleStats)

	fmt.Printf("Server running on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}

// ── /api/search ───────────────────────────────────────────────────
// Returns {"query": "…", "results": […]}
// score is a float64 (not a string) so the Qt client can parse it directly.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	type resultItem struct {
		Path      string  `json:"path"`
		Hash      string  `json:"hash"`
		Caption   string  `json:"caption"`
		Category  string  `json:"category"`
		Score     float64 `json:"score"` // ← float, not string
		Thumbnail string  `json:"thumbnail"`
	}

	var items []resultItem

	if q != "" {
		expander := search.NewQueryExpander()
		expanded := expander.Expand(q)
		emb, err := s.embed.GetEmbedding(expanded)
		if err == nil && len(emb) > 0 {
			results := s.index.Search(emb, 50)
			for _, res := range results {
				items = append(items, resultItem{
					Path:      res.Path,
					Hash:      res.Hash,
					Caption:   res.Caption,
					Category:  res.Category,
					Score:     float64(res.Score),
					Thumbnail: "/api/thumbnail/" + res.Hash + "?size=small",
				})
			}
		}
	}

	// Fallback: return recent items with score=0 when embed server is down
	// or query is empty.
	if len(items) == 0 {
		rows, err := s.db.Query(
			"SELECT id, path, hash, caption, category FROM images ORDER BY id DESC LIMIT 50")
		if err != nil {
			http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var v search.Vector
			rows.Scan(&v.ID, &v.Path, &v.Hash, &v.Caption, &v.Category)
			items = append(items, resultItem{
				Path:      v.Path,
				Hash:      v.Hash,
				Caption:   v.Caption,
				Category:  v.Category,
				Score:     0,
				Thumbnail: "/api/thumbnail/" + v.Hash + "?size=small",
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"query":   q,
		"results": items,
	})
}

// ── /api/similar  (POST {"image":"<base64>"}) ─────────────────────
func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Image string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}

	imgData, err := base64.StdEncoding.DecodeString(req.Image)
	if err != nil {
		http.Error(w, `{"error":"bad base64"}`, http.StatusBadRequest)
		return
	}

	// Cache lookup by content hash (first 8 bytes → 16 hex chars)
	sum     := sha256.Sum256(imgData)
	hashStr := fmt.Sprintf("%x", sum[:8])

	var emb []float32
	if cached, ok := snapCache.Load(hashStr); ok {
		emb = cached.([]float32)
	} else {
		analysis, err := s.ollama.AnalyzeImage(imgData)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		emb, err = s.embed.GetEmbedding(analysis.Caption)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		snapCache.Store(hashStr, emb)
	}

	results := s.index.Search(emb, 20)

	type item struct {
		Hash      string  `json:"hash"`
		Caption   string  `json:"caption"`
		Category  string  `json:"category"`
		Score     float64 `json:"score"`
		Thumbnail string  `json:"thumbnail"`
	}
	var out []item
	for _, res := range results {
		out = append(out, item{
			Hash:      res.Hash,
			Caption:   res.Caption,
			Category:  res.Category,
			Score:     float64(res.Score),
			Thumbnail: "/api/thumbnail/" + res.Hash + "?size=small",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"results": out})
}

// ── /api/images ───────────────────────────────────────────────────
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	cat  := r.URL.Query().Get("category")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit  := 50
	offset := (page - 1) * limit

	var rows *sql.Rows
	var err error
	if cat != "" {
		rows, err = s.db.Query(
			"SELECT path, hash, caption, category FROM images WHERE category = ? ORDER BY id DESC LIMIT ? OFFSET ?",
			cat, limit, offset)
	} else {
		rows, err = s.db.Query(
			"SELECT path, hash, caption, category FROM images ORDER BY id DESC LIMIT ? OFFSET ?",
			limit, offset)
	}
	if err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type item struct {
		Path      string `json:"path"`
		Hash      string `json:"hash"`
		Caption   string `json:"caption"`
		Category  string `json:"category"`
		Thumbnail string `json:"thumbnail"`
	}
	var results []item
	for rows.Next() {
		var it item
		rows.Scan(&it.Path, &it.Hash, &it.Caption, &it.Category)
		it.Thumbnail = "/api/thumbnail/" + it.Hash + "?size=small"
		results = append(results, it)
	}
	if results == nil {
		results = []item{} // return [] not null
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// ── /api/image/<hash> ─────────────────────────────────────────────
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/api/image/")
	var path string
	s.db.QueryRow("SELECT path FROM images WHERE hash = ?", hash).Scan(&path)
	if path == "" {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, path)
}

// ── /api/thumbnail/<hash>?size=small|medium ───────────────────────
func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/api/thumbnail/")
	size := r.URL.Query().Get("size")
	if size == "" {
		size = "small"
	}
	if len(hash) < 2 {
		http.Error(w, `{"error":"bad hash"}`, http.StatusBadRequest)
		return
	}
	path := filepath.Join(s.cacheDir, "thumbnails", size, hash[:2], hash+".jpg")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	http.ServeFile(w, r, path)
}

// ── /api/ingest  (multipart POST) ────────────────────────────────
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, `{"error":"no image"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	tmpPath := filepath.Join(os.TempDir(), header.Filename)
	f, _ := os.Create(tmpPath)
	f.ReadFrom(file)
	f.Close()

	meta, err := validate.PrepareImage(tmpPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}
	analysis, err := s.ollama.AnalyzeImage(meta.VLMBytes)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	tags := strings.Join(analysis.Tags, " ")
	s.db.Exec(
		`INSERT INTO images (path, hash, caption, category, tags, has_text, has_faces, orientation, tier, processed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.Path, meta.Hash, analysis.Caption, analysis.Category,
		tags, boolToInt(analysis.HasText), boolToInt(analysis.HasFaces),
		analysis.Orientation, "upload", time.Now(),
	)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"hash":     meta.Hash,
		"caption":  analysis.Caption,
		"category": analysis.Category,
	})
}

// ── /api/stats ────────────────────────────────────────────────────
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	var total, processed, failed int
	s.db.QueryRow("SELECT COUNT(*) FROM images").Scan(&total)
	s.db.QueryRow("SELECT COUNT(*) FROM images WHERE category = 'failed'").Scan(&failed)
	processed = total - failed

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"total":     total,
		"processed": processed,
		"failed":    failed,
	})
}

// ── /api/health ───────────────────────────────────────────────────
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
