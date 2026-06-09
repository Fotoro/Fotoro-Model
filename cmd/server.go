package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fotoro/internal/db"
	"fotoro/internal/ollama"
)

type Server struct {
	db       *db.DB
	cacheDir string
	ollama   *ollama.Client
}

func RunServer(addr, dbPath, model string) {
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	s := &Server{
		db:       database,
		cacheDir: filepath.Join(filepath.Dir(dbPath), ".cache"),
		ollama:   ollama.NewClient("http://localhost:11434", model),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/images", s.handleList)
	mux.HandleFunc("/api/image/", s.handleImage)
	mux.HandleFunc("/api/thumbnail/", s.handleThumbnail)
	mux.HandleFunc("/api/query", s.handleQuery)
	mux.HandleFunc("/api/health", s.handleHealth)

	fmt.Printf("Server running on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, `{"error":"missing q"}`, http.StatusBadRequest)
		return
	}

	rows, err := s.db.Query(
		"SELECT path, caption, category FROM fts_captions WHERE fts_captions MATCH ? ORDER BY rank LIMIT 50",
		q,
	)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var path, caption, category string
		rows.Scan(&path, &caption, &category)
		results = append(results, map[string]interface{}{
			"path":     path,
			"caption":  caption,
			"category": category,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
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

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}

	// Phase 1: direct FTS5 search (fastest, no LLM latency)
	rows, err := s.db.Query(
		"SELECT path, caption, category FROM fts_captions WHERE fts_captions MATCH ? ORDER BY rank LIMIT 50",
		req.Query,
	)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var path, caption, category string
		rows.Scan(&path, &caption, &category)
		results = append(results, map[string]interface{}{
			"path":     path,
			"caption":  caption,
			"category": category,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"query":   req.Query,
		"results": results,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}