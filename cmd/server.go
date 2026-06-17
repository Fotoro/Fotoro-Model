package cmd

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fotoro/internal/auth"
	"fotoro/internal/db"
	"fotoro/internal/ollama"
	"fotoro/internal/search"
	"fotoro/internal/system"
	"fotoro/internal/tailscale"
)

type Server struct {
	db           *db.DB
	cacheDir     string
	ollama       *ollama.Client
	embed        *ollama.EmbedClient
	index        *search.Index
	supabaseAuth *auth.SupabaseAuth
	tailscale    *tailscale.Manager
	serverMu     sync.RWMutex
	running      bool
	authMu           sync.RWMutex
	authAccessToken  string
	authRefreshToken string
	authUser         *auth.User
}

var snapCache sync.Map

func RunServer(addr, dbPath, model string) {
	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseAnon := os.Getenv("SUPABASE_ANON_KEY")
	var supabaseAuth *auth.SupabaseAuth
	if supabaseURL != "" && supabaseAnon != "" {
		supabaseAuth = auth.NewSupabaseAuth(supabaseURL, supabaseAnon)
		if err := supabaseAuth.Initialize(); err != nil {
			fmt.Printf("[WARN] Supabase auth init failed: %v\\n", err)
			supabaseAuth = nil
		} else {
			fmt.Println("[AUTH] Supabase JWT verification ready")
		}
	}

	idx := search.NewIndex()
	if err := idx.LoadFromDB(database.DB); err != nil {
		fmt.Printf("[WARN] Could not load vectors: %v\\n", err)
	} else {
		fmt.Printf("[INIT] Loaded %d vectors into memory\\n", idx.Len())
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := idx.LoadFromDB(database.DB); err != nil {
				fmt.Printf("[WARN] Index reload failed: %v\\n", err)
			}
		}
	}()

	s := &Server{
		db:           database,
		cacheDir:     filepath.Join(filepath.Dir(dbPath), ".cache"),
		ollama:       ollama.NewClient("", model),
		embed:        ollama.NewEmbedClient(),
		index:        idx,
		supabaseAuth: supabaseAuth,
		tailscale:    tailscale.NewManager(),
	}
	s.loadStoredSession()

	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/auth/session", s.handleAuthSession)
	mux.HandleFunc("/api/setup/status", s.handleSetupStatus)
	mux.HandleFunc("/auth/setup", s.handleAuthSetupPage)
	mux.HandleFunc("/auth/callback", s.handleAuthCallbackPage)
	mux.HandleFunc("/api/setup/complete", s.handleSetupComplete)

	// Auth required
	mux.HandleFunc("/api/auth/me", s.withAuth(s.handleMe))
	mux.HandleFunc("/api/server/start", s.withAuth(s.handleServerStart))
	mux.HandleFunc("/api/server/stop", s.withAuth(s.handleServerStop))
	mux.HandleFunc("/api/server/status", s.withAuth(s.handleServerStatus))
	mux.HandleFunc("/api/tailscale/status", s.withAuth(s.handleTailscaleStatus))
	mux.HandleFunc("/api/tailscale/connect", s.withAuth(s.handleTailscaleConnect))
	mux.HandleFunc("/api/tailscale/disconnect", s.withAuth(s.handleTailscaleDisconnect))
	mux.HandleFunc("/api/tailscale/info", s.withAuth(s.handleTailscaleInfo))
	mux.HandleFunc("/api/search", s.withAuth(s.handleSearch))
	mux.HandleFunc("/api/similar", s.withAuth(s.handleSimilar))
	mux.HandleFunc("/api/images", s.withAuth(s.handleList))
	mux.HandleFunc("/api/timeline", s.withAuth(s.handleTimeline))
	mux.HandleFunc("/api/image/", s.withAuth(s.handleImage))
	mux.HandleFunc("/api/thumbnail/", s.withAuth(s.handleThumbnail))
	mux.HandleFunc("/api/stats", s.withAuth(s.handleStats))
	mux.HandleFunc("/api/web/upload", s.withAuth(s.handleWebUpload))
	mux.HandleFunc("/api/web/upload/status", s.withAuth(s.handleUploadStatus))
	mux.HandleFunc("/api/scheduler/run", s.withAuth(s.handleSchedulerRun))
	mux.HandleFunc("/api/scheduler/status", s.withAuth(s.handleSchedulerStatus))
	mux.HandleFunc("/api/system/memory", s.withAuth(s.handleMemoryStatus))

	fmt.Printf("[SERVER] Running on %s\\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.supabaseAuth == nil {
			next(w, r)
			return
		}
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
		user, err := s.supabaseAuth.VerifyToken(parts[1])
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusUnauthorized)
			return
		}
		ctx := auth.WithUser(r.Context(), user)
		next(w, r.WithContext(ctx))
	}
}

// ── HANDLERS ─────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	s.authMu.RLock()
	user := s.authUser
	authenticated := s.authAccessToken != "" && user != nil
	s.authMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"configured":    s.authConfigured(),
		"authenticated": authenticated,
		"user":          user,
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromRequest(r)
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func (s *Server) handleServerStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	modelPath := os.Getenv("FOTORO_MODEL_PATH")
	if modelPath == "" {
		modelPath = "./models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf"
	}
	ok, msg := system.CanStartLLM(modelPath)
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, msg), http.StatusServiceUnavailable)
		return
	}
	s.serverMu.Lock()
	s.running = true
	s.serverMu.Unlock()
	system.MonitorSwap(80.0, func() {
		s.ollama.StopServer()
		s.serverMu.Lock()
		s.running = false
		s.serverMu.Unlock()
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started", "memory_check": msg})
}

func (s *Server) handleServerStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	s.ollama.StopServer()
	s.serverMu.Lock()
	s.running = false
	s.serverMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

func (s *Server) handleServerStatus(w http.ResponseWriter, r *http.Request) {
	s.serverMu.RLock()
	running := s.running
	s.serverMu.RUnlock()
	mem, _ := system.GetMemoryStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"running": running,
		"memory":  mem,
	})
}

func (s *Server) handleTailscaleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"installed": s.tailscale.IsInstalled(),
		"running":   s.tailscale.IsRunning(),
	})
}

func (s *Server) handleTailscaleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		AuthKey string `json:"auth_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	if req.AuthKey == "" {
		req.AuthKey = os.Getenv("TAILSCALE_AUTH_KEY")
	}
	if err := s.tailscale.Up(req.AuthKey, []string{"tag:fotoro"}); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	ip, _ := s.tailscale.GetTailscaleIP()
	tailnet, _ := s.tailscale.GetTailnetName()
	s.db.UpdateServerConfig(map[string]interface{}{
		"tailscale_enabled": 1,
		"tailscale_ip":      ip,
		"tailnet_name":      tailnet,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "connected", "ip": ip})
}

func (s *Server) handleTailscaleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	if err := s.tailscale.Down(); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	s.db.UpdateServerConfig(map[string]interface{}{
		"tailscale_enabled": 0,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
}

func (s *Server) handleTailscaleInfo(w http.ResponseWriter, r *http.Request) {
	ip, _ := s.tailscale.GetTailscaleIP()
	tailnet, _ := s.tailscale.GetTailnetName()
	magicDNS, _ := s.tailscale.GetMagicDNS()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ip":        ip,
		"tailnet":   tailnet,
		"magic_dns": magicDNS,
		"installed": s.tailscale.IsInstalled(),
		"running":   s.tailscale.IsRunning(),
	})
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`
		SELECT strftime('%Y-%m', COALESCE(taken_at, processed_at, created_at)) as month, COUNT(*)
		FROM images GROUP BY month HAVING month IS NOT NULL ORDER BY month DESC
	`)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var month string
		var count int
		rows.Scan(&month, &count)
		result[month] = count
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	type resultItem struct {
		Path      string  `json:"path"`
		Hash      string  `json:"hash"`
		Caption   string  `json:"caption"`
		Category  string  `json:"category"`
		Score     float64 `json:"score"`
		Thumbnail string  `json:"thumbnail"`
	}
	var items []resultItem

	if q != "" {
		scores := make(map[string]float64)
		expander := search.NewQueryExpander()
		expanded := expander.Expand(q)
		emb, err := s.embed.GetEmbedding(expanded)
		if err == nil && len(emb) > 0 {
			for _, res := range s.index.Search(emb, 100) {
				scores[res.Hash] = float64(res.Score) * 0.6
			}
		}
		safeQ := strings.ReplaceAll(q, `"`, `""`)
		rows, err := s.db.Query(
			`SELECT rowid, rank FROM fts_captions WHERE fts_captions MATCH ? ORDER BY rank LIMIT 100`, safeQ)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var rowid int
				var rank float64
				rows.Scan(&rowid, &rank)
				var hash string
				s.db.QueryRow("SELECT hash FROM images WHERE id = ?", rowid).Scan(&hash)
				if hash == "" {
					continue
				}
				ftsScore := 1.0 / (1.0 + math.Abs(rank)/10.0)
				scores[hash] += ftsScore * 0.4
			}
		}
		type scored struct {
			hash  string
			score float64
		}
		var list []scored
		for h, sc := range scores {
			if sc >= 0.12 {
				list = append(list, scored{h, sc})
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
		if len(list) > 50 {
			list = list[:50]
		}
		for _, si := range list {
			var it resultItem
			if err := s.db.QueryRow(
				"SELECT path, hash, caption, category FROM images WHERE hash = ?", si.hash).
				Scan(&it.Path, &it.Hash, &it.Caption, &it.Category); err != nil {
				continue
			}
			it.Score = math.Round(si.score*100) / 100
			it.Thumbnail = "/api/thumbnail/" + it.Hash + "?size=small"
			items = append(items, it)
		}
	}

	if len(items) == 0 && q == "" {
		rows, err := s.db.Query("SELECT path, hash, caption, category FROM images ORDER BY id DESC LIMIT 50")
		if err != nil {
			http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var it resultItem
			rows.Scan(&it.Path, &it.Hash, &it.Caption, &it.Category)
			it.Thumbnail = "/api/thumbnail/" + it.Hash + "?size=small"
			items = append(items, it)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"query":   q,
		"count":   len(items),
		"results": items,
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
	sum := sha256.Sum256(imgData)
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

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	cat := r.URL.Query().Get("category")
	sortParam := r.URL.Query().Get("sort")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit := 50
	offset := (page - 1) * limit
	orderBy := "id DESC"
	if sortParam == "date" {
		orderBy = "COALESCE(taken_at, processed_at, created_at) DESC"
	}

	var rows *sql.Rows
	var err error
	if cat != "" {
		rows, err = s.db.Query(
			"SELECT path, hash, caption, category FROM images WHERE category = ? ORDER BY "+orderBy+" LIMIT ? OFFSET ?",
			cat, limit, offset)
	} else {
		rows, err = s.db.Query(
			"SELECT path, hash, caption, category FROM images ORDER BY "+orderBy+" LIMIT ? OFFSET ?",
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
		results = []item{}
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

func (s *Server) handleWebUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, `{"error":"no image"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	uploadsDir := filepath.Join(filepath.Dir(os.Getenv("FOTORO_DB")), "uploads")
	os.MkdirAll(uploadsDir, 0755)

	timestamp := time.Now().Format("20060102_150405")
	safeName := sanitizeFilename(header.Filename)
	stagingPath := filepath.Join(uploadsDir, fmt.Sprintf("%s_%s", timestamp, safeName))

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"path":    stagingPath,
		"size":    size,
		"message": "Image saved. Run 'fotoro scheduler' to process.",
	})
}

func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleSchedulerRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "processing started"})
}

func (s *Server) handleSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"pending": 0,
		"status":  "idle",
	})
}

func (s *Server) handleMemoryStatus(w http.ResponseWriter, r *http.Request) {
	mem, err := system.GetMemoryStatus()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	modelPath := os.Getenv("FOTORO_MODEL_PATH")
	if modelPath == "" {
		modelPath = "./models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf"
	}
	canStart, msg := system.CanStartLLM(modelPath)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"memory":      mem,
		"can_start":   canStart,
		"llm_message": msg,
	})
}
