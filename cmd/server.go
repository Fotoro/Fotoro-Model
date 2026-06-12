package cmd

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	var results []search.Result

	if q != "" {
		expander := search.NewQueryExpander()
		expanded := expander.Expand(q)
		emb, err := s.embed.GetEmbedding(expanded)
		if err == nil && len(emb) > 0 {
			results = s.index.Search(emb, 50)
		}
	}

	if len(results) == 0 {
		rows, err := s.db.Query("SELECT id, path, hash, caption, category FROM images ORDER BY id DESC LIMIT 50")
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var v search.Vector
			rows.Scan(&v.ID, &v.Path, &v.Hash, &v.Caption, &v.Category)
			results = append(results, search.Result{Vector: v, Score: 0})
		}
	}

	var out []map[string]interface{}
	for _, res := range results {
		out = append(out, map[string]interface{}{
			"path":      res.Path,
			"hash":      res.Hash,
			"caption":   res.Caption,
			"category":  res.Category,
			"score":     fmt.Sprintf("%.3f", res.Score),
			"thumbnail": "/api/thumbnail/" + res.Hash + "?size=small",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"query":   q,
		"results": out,
	})
}

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

	analysis, err := s.ollama.AnalyzeImage(imgData)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	emb, err := s.embed.GetEmbedding(analysis.Caption)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	results := s.index.Search(emb, 20)
	var out []map[string]interface{}
	for _, res := range results {
		out = append(out, map[string]interface{}{
			"path":      res.Path,
			"hash":      res.Hash,
			"caption":   res.Caption,
			"category":  res.Category,
			"score":     fmt.Sprintf("%.3f", res.Score),
			"thumbnail": "/api/thumbnail/" + res.Hash + "?size=small",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"query_image_caption": analysis.Caption,
		"results":             out,
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	cat := r.URL.Query().Get("category")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit := 50
	offset := (page - 1) * limit

	var rows *sql.Rows
	var err error
	if cat != "" {
		rows, err = s.db.Query(
			"SELECT path, hash, caption, category FROM images WHERE category = ? ORDER BY id DESC LIMIT ? OFFSET ?",
			cat, limit, offset,
		)
	} else {
		rows, err = s.db.Query(
			"SELECT path, hash, caption, category FROM images ORDER BY id DESC LIMIT ? OFFSET ?",
			limit, offset,
		)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var path, hash, caption, category string
		rows.Scan(&path, &hash, &caption, &category)
		results = append(results, map[string]interface{}{
			"path":      path,
			"hash":      hash,
			"caption":   caption,
			"category":  category,
			"thumbnail": "/api/thumbnail/" + hash + "?size=small",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

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

func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/api/thumbnail/")
	size := r.URL.Query().Get("size")
	if size == "" {
		size = "small"
	}
	path := filepath.Join(s.cacheDir, "thumbnails", size, hash[:2], hash+".jpg")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	http.ServeFile(w, r, path)
}

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

	tags := ""
	if len(analysis.Tags) > 0 {
		tags = strings.Join(analysis.Tags, " ")
	}
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

var snapCache sync.Map // hash → embedding

func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
    // ... decode image ...
    
    // Check cache first
    imgHash := sha256.Sum256(imgData)
    hashStr := fmt.Sprintf("%x", imgHash[:8])
    
    if cached, ok := snapCache.Load(hashStr); ok {
        results := s.index.Search(cached.([]float32), 20)
        // return cached results
    }
    
    // Otherwise: analyze, embed, cache, search
    analysis, _ := s.ollama.AnalyzeImage(imgData)
    emb, _ := s.embed.GetEmbedding(analysis.Caption)
    snapCache.Store(hashStr, emb)
    
    results := s.index.Search(emb, 20)
    // ...
}

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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
