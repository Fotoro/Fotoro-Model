package cmd

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"fotoro/internal/auth"
	"fotoro/internal/db"
	"fotoro/internal/media"
	"fotoro/internal/ollama"
	"fotoro/internal/scheduler"
	"fotoro/internal/search"
	"fotoro/internal/system"
	"fotoro/internal/tailscale"
)

type Server struct {
	db               *db.DB
	cacheDir         string
	ollama           *ollama.Client
	embed            *ollama.EmbedClient
	index            *search.Index
	queue            *scheduler.JobQueue
	media            *media.Service
	supabaseAuth     *auth.SupabaseAuth
	tailscale        *tailscale.Manager
	model            string
	metricsMu        sync.Mutex
	serverMu         sync.RWMutex
	running          bool
	authMu           sync.RWMutex
	authAccessToken  string
	authRefreshToken string
	authUser         *auth.User
}

var snapCache sync.Map

func RunServer(addr, dbPath, model string) {
	LoadDotEnv()

	database, err := db.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer database.Close()

	queue := scheduler.NewJobQueue(database, scheduler.ScheduleConfig{
		Enabled:            false,
		ProcessTime:        "02:00",
		MaxConcurrent:      1,
		MaxPerBatch:        25,
		ProcessScreenshots: true,
		ProcessDocuments:   true,
		ProcessPhotos:      true,
	})
	if err := queue.InitDB(); err != nil {
		fmt.Printf("[WARN] Queue init failed: %v\\n", err)
	} else if err := queue.LoadConfig(); err != nil {
		fmt.Printf("[WARN] Queue config load failed: %v\\n", err)
	}
	mediaService, err := media.NewService(database, dbPath)
	if err != nil {
		panic(fmt.Errorf("media service: %w", err))
	}

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
		queue:        queue,
		media:        mediaService,
		supabaseAuth: supabaseAuth,
		tailscale:    tailscale.NewManager(),
		model:        model,
	}
	s.loadStoredSession()
	go func() {
		// Keep the local SQLite snapshot fresh even when no dashboard is open.
		_, _ = s.dashboardMetrics(context.Background())
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			_, _ = s.dashboardMetrics(context.Background())
		}
	}()

	// Register node with website dashboard + periodic heartbeat
	if complete, _ := database.IsSetupComplete(); complete {
		go func() {
			time.Sleep(2 * time.Second)
			s.syncNodeToCloud()
		}()
		go s.runNodeHeartbeat()
	}

	mux := http.NewServeMux()

	// Public health probe. Setup and local-session endpoints are deliberately
	// loopback-only; they must never be reachable through a reverse proxy.
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/auth/status", s.localOnly(s.handleAuthStatus))
	mux.HandleFunc("/api/auth/session", s.localOnly(s.handleAuthSession))
	mux.HandleFunc("/api/auth/login-url", s.localOnly(s.handleAuthLoginURL))
	mux.HandleFunc("/api/setup/status", s.localOnly(s.handleSetupStatus))
	mux.HandleFunc("/auth/setup", s.localOnly(s.handleAuthSetupPage))
	mux.HandleFunc("/auth/callback", s.localOnly(s.handleAuthCallbackPage))
	mux.HandleFunc("/api/setup/complete", s.localOnly(s.handleSetupComplete))

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
	mux.HandleFunc("/api/ingest/folder", s.withAuth(s.handleIngestFolder))
	mux.HandleFunc("/api/scheduler/run", s.withAuth(s.handleSchedulerRun))
	mux.HandleFunc("/api/scheduler/status", s.withAuth(s.handleSchedulerStatus))
	mux.HandleFunc("/api/scheduler/config", s.withAuth(s.handleSchedulerConfig))
	mux.HandleFunc("/api/system/memory", s.withAuth(s.handleMemoryStatus))

	registerGalleryRoutes(mux, s)
	registerMediaV1Routes(mux, s)
	registerStorageRoutes(mux, s)
	registerDiagnosticsRoutes(mux, s)

	fmt.Printf("[SERVER] Running on %s\\n", addr)
	queue.StartDaemon(model)
	s.startDerivativeWorker()
	s.startBackupWorker()
	go ensureTailscaleExpose(s, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Explicit opt-in for isolated local development only.
		if s.isLocalRequest(r) && localAuthBypassEnabled() {
			ownerID, _ := s.boundOwnerID()
			if ownerID == "" {
				ownerID = "local"
			}
			next(w, r.WithContext(auth.WithUser(r.Context(), &auth.User{ID: ownerID})))
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
		user, err := s.verifyBearerToken(parts[1])
		if err != nil {
			status := http.StatusUnauthorized
			if s.supabaseAuth == nil && !strings.HasPrefix(parts[1], deviceTokenPrefix) {
				status = http.StatusServiceUnavailable
			}
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), status)
			return
		}
		if err := s.authorizeUser(r, user); err != nil {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		ctx := auth.WithUser(r.Context(), user)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) isLocalRequest(r *http.Request) bool {
	if r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("X-Forwarded-Host") != "" || r.Header.Get("X-Forwarded-Proto") != "" {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	hostIsLoopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if !hostIsLoopback {
		return false
	}
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		parsed := net.ParseIP(ip)
		return parsed != nil && parsed.IsLoopback()
	}
	parsed := net.ParseIP(strings.Trim(r.RemoteAddr, "[]"))
	return parsed != nil && parsed.IsLoopback()
}

func (s *Server) localOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isLocalRequest(r) {
			http.Error(w, `{"error":"local access only"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Server) hasLocalSession() bool {
	if s.db == nil {
		return false
	}
	sess, err := s.db.GetAuthSession()
	return err == nil && sess != nil && sess.AccessToken != ""
}

// ── HANDLERS ─────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	LoadDotEnv()
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
	if s.tailscale.IsRunning() {
		ip, _ := s.tailscale.GetTailscaleIP()
		magicDNS, _ := s.tailscale.GetMagicDNS()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    "connected",
			"ip":        ip,
			"magic_dns": magicDNS,
		})
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
	if req.AuthKey == "" {
		http.Error(w, `{"error":"Tailscale not connected — run ./fotoro setup in a terminal"}`, http.StatusBadRequest)
		return
	}
	if err := s.tailscale.Up(req.AuthKey, []string{"tag:fotoro"}); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	ip, _ := s.tailscale.GetTailscaleIP()
	tailnet, _ := s.tailscale.GetTailnetName()
	magicDNS, _ := s.tailscale.GetMagicDNS()
	s.db.UpdateServerConfig(map[string]interface{}{
		"tailscale_enabled": 1,
		"tailscale_ip":      ip,
		"tailnet_name":      tailnet,
		"tailnet_url":       magicDNS,
	})
	go s.syncNodeToCloud()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "connected",
		"ip":        ip,
		"magic_dns": magicDNS,
	})
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

func (s *Server) runNodeHeartbeat() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !s.tailscale.IsRunning() {
			continue
		}
		s.syncNodeToCloud()
	}
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
		AddedAt   string  `json:"added_at,omitempty"`
	}
	var items []resultItem

	if q != "" {
		type scored struct {
			hash  string
			score float64
			added time.Time
		}
		scores := make(map[string]*scored)
		addScore := func(hash string, score float64, added time.Time) {
			if hash == "" {
				return
			}
			if existing, ok := scores[hash]; ok {
				existing.score += score
				if existing.added.IsZero() || added.After(existing.added) {
					existing.added = added
				}
				return
			}
			scores[hash] = &scored{hash: hash, score: score, added: added}
		}

		expander := search.NewQueryExpander()
		expanded := expander.Expand(q)
		emb, err := s.embed.GetEmbedding(expanded)
		if err == nil && len(emb) > 0 {
			for _, res := range s.index.Search(emb, 100) {
				addScore(res.Hash, float64(res.Score)*0.55, time.Time{})
			}
		}

		terms := searchTerms(q + " " + expanded)
		if ftsQ := ftsMatchQuery(terms); ftsQ != "" {
			rows, err := s.db.Query(`
				SELECT images.hash, fts_captions.rank, COALESCE(images.processed_at, images.created_at) as added
				FROM fts_captions
				JOIN images ON images.id = fts_captions.rowid
				WHERE fts_captions MATCH ?
				ORDER BY fts_captions.rank
				LIMIT 200`, ftsQ)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var hash string
					var rank float64
					var added sql.NullTime
					rows.Scan(&hash, &rank, &added)
					ftsScore := 1.0 / (1.0 + math.Abs(rank)/8.0)
					addedAt := time.Time{}
					if added.Valid {
						addedAt = added.Time
					}
					addScore(hash, ftsScore*0.65, addedAt)
				}
			}
		}

		if len(terms) > 0 {
			clauses := make([]string, 0, len(terms)*3)
			args := make([]interface{}, 0, len(terms)*3+1)
			for _, term := range terms {
				like := "%" + term + "%"
				clauses = append(clauses,
					"LOWER(COALESCE(caption, '')) LIKE ?",
					"LOWER(COALESCE(tags, '')) LIKE ?",
					"LOWER(COALESCE(ocr_text, '')) LIKE ?",
				)
				args = append(args, like, like, like)
			}
			args = append(args, 300)
			rows, err := s.db.Query(`
				SELECT hash, caption, category, tags, ocr_text, COALESCE(processed_at, created_at) as added
				FROM images
				WHERE `+strings.Join(clauses, " OR ")+`
				ORDER BY COALESCE(processed_at, created_at) DESC
				LIMIT ?`, args...)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var hash, caption, category string
					var tags, ocr sql.NullString
					var added sql.NullTime
					rows.Scan(&hash, &caption, &category, &tags, &ocr, &added)
					text := strings.ToLower(caption + " " + tags.String + " " + ocr.String + " " + category)
					score := lexicalScore(text, terms)
					addedAt := time.Time{}
					if added.Valid {
						addedAt = added.Time
					}
					addScore(hash, score*0.45, addedAt)
				}
			}
		}

		list := make([]scored, 0, len(scores))
		for _, sc := range scores {
			if sc.score >= 0.10 {
				list = append(list, *sc)
			}
		}
		sort.Slice(list, func(i, j int) bool {
			if math.Abs(list[i].score-list[j].score) < 0.03 {
				return list[i].added.After(list[j].added)
			}
			return list[i].score > list[j].score
		})
		if len(list) > 50 {
			list = list[:50]
		}
		for _, si := range list {
			var it resultItem
			var added sql.NullTime
			if err := s.db.QueryRow(
				"SELECT path, hash, caption, category, COALESCE(processed_at, created_at) FROM images WHERE hash = ?", si.hash).
				Scan(&it.Path, &it.Hash, &it.Caption, &it.Category, &added); err != nil {
				continue
			}
			it.Score = math.Round(si.score*100) / 100
			it.Thumbnail = "/api/thumbnail/" + it.Hash + "?size=small"
			if validDBTime(added) {
				it.AddedAt = added.Time.UTC().Format(time.RFC3339)
			}
			items = append(items, it)
		}
	}

	if len(items) == 0 && q == "" {
		rows, err := s.db.Query(`
			SELECT path, hash, caption, category, COALESCE(processed_at, created_at) as added
			FROM images ORDER BY added DESC, id DESC LIMIT 50`)
		if err != nil {
			http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var it resultItem
			var added sql.NullTime
			rows.Scan(&it.Path, &it.Hash, &it.Caption, &it.Category, &added)
			it.Thumbnail = "/api/thumbnail/" + it.Hash + "?size=small"
			if validDBTime(added) {
				it.AddedAt = added.Time.UTC().Format(time.RFC3339)
			}
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

func searchTerms(q string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, raw := range strings.Fields(strings.ToLower(q)) {
		term := strings.TrimFunc(raw, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len(term) < 2 || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
	}
	return out
}

func ftsMatchQuery(terms []string) string {
	if len(terms) == 0 {
		return ""
	}
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		clean := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
				return r
			}
			return -1
		}, term)
		if clean != "" {
			parts = append(parts, clean+"*")
		}
	}
	return strings.Join(parts, " OR ")
}

func lexicalScore(text string, terms []string) float64 {
	if text == "" || len(terms) == 0 {
		return 0
	}
	var hits int
	for _, term := range terms {
		if strings.Contains(text, term) {
			hits++
		}
	}
	if hits == 0 {
		return 0
	}
	return 0.35 + 0.65*(float64(hits)/float64(len(terms)))
}

func validDBTime(t sql.NullTime) bool {
	return t.Valid && !t.Time.IsZero() && t.Time.Year() > 1
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
	if perPage, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && perPage > 0 && perPage <= 200 {
		limit = perPage
	}
	offset := (page - 1) * limit
	orderBy := "COALESCE(processed_at, created_at) DESC, id DESC"
	if sortParam == "date" || sortParam == "taken" || sortParam == "captured" {
		orderBy = "COALESCE(taken_at, processed_at, created_at) DESC"
	}

	var rows *sql.Rows
	var err error
	if cat != "" {
		rows, err = s.db.Query(
			"SELECT path, hash, caption, category, COALESCE(processed_at, created_at) FROM images WHERE category = ? ORDER BY "+orderBy+" LIMIT ? OFFSET ?",
			cat, limit, offset)
	} else {
		rows, err = s.db.Query(
			"SELECT path, hash, caption, category, COALESCE(processed_at, created_at) FROM images ORDER BY "+orderBy+" LIMIT ? OFFSET ?",
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
		AddedAt   string `json:"added_at,omitempty"`
	}
	var results []item
	for rows.Next() {
		var it item
		var added sql.NullTime
		rows.Scan(&it.Path, &it.Hash, &it.Caption, &it.Category, &added)
		it.Thumbnail = "/api/thumbnail/" + it.Hash + "?size=small"
		if validDBTime(added) {
			it.AddedAt = added.Time.UTC().Format(time.RFC3339)
		}
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
		size = "medium"
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
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	metrics, err := s.dashboardMetrics(r.Context())
	if err != nil {
		http.Error(w, `{"error":"metrics unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, metrics)
}

type statsDiskStat struct {
	Total uint64
	Free  uint64
}

func statsDiskUsage(path string) (statsDiskStat, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return statsDiskStat{}, err
	}
	return statsDiskStat{
		Total: st.Blocks * uint64(st.Bsize),
		Free:  st.Bavail * uint64(st.Bsize),
	}, nil
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

	dbPath := os.Getenv("FOTORO_DB")
	if dbPath == "" {
		dbPath = "fotoro.db"
	}
	uploadsDir := filepath.Join(filepath.Dir(dbPath), "uploads")
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

	if s.queue == nil {
		http.Error(w, `{"error":"upload queue not available"}`, http.StatusServiceUnavailable)
		return
	}
	upload, err := s.queue.AddUpload(stagingPath, "web", header.Filename, header.Header.Get("Content-Type"), size)
	if err != nil {
		_ = os.Remove(stagingPath)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusConflict)
		return
	}
	if upload.Status == "duplicate" {
		_ = os.Remove(stagingPath)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"upload_id": upload.ID,
		"hash":      upload.Hash,
		"status":    upload.Status,
		"path":      stagingPath,
		"size":      size,
		"message":   "Image queued for processing.",
	})
}

func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	if s.queue == nil {
		http.Error(w, `{"error":"upload queue not available"}`, http.StatusServiceUnavailable)
		return
	}
	stats, err := s.queue.GetStats()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func (s *Server) handleIngestFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.queue == nil {
		http.Error(w, `{"error":"upload queue not available"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		http.Error(w, `{"error":"missing path"}`, http.StatusBadRequest)
		return
	}
	info, err := os.Stat(req.Path)
	if err != nil || !info.IsDir() {
		http.Error(w, `{"error":"folder not found"}`, http.StatusBadRequest)
		return
	}

	files := collectImages(req.Path)
	var queued, duplicates, skipped int
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			skipped++
			continue
		}
		upload, err := s.queue.AddUpload(path, "desktop-folder", filepath.Base(path), contentTypeFromName(path), info.Size())
		if err != nil {
			skipped++
			continue
		}
		if upload.Status == "duplicate" {
			duplicates++
			continue
		}
		queued++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"scanned":    len(files),
		"queued":     queued,
		"duplicates": duplicates,
		"skipped":    skipped,
	})
}

func (s *Server) handleSchedulerRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.queue == nil {
		http.Error(w, `{"error":"upload queue not available"}`, http.StatusServiceUnavailable)
		return
	}
	if s.queue.IsProcessing() {
		http.Error(w, `{"error":"scheduler already processing"}`, http.StatusConflict)
		return
	}
	go func() {
		if err := s.queue.ProcessNow(s.model); err != nil {
			fmt.Printf("[SCHEDULER] Run failed: %v\\n", err)
		}
	}()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "processing started"})
}

func (s *Server) handleSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	if s.queue == nil {
		http.Error(w, `{"error":"upload queue not available"}`, http.StatusServiceUnavailable)
		return
	}
	stats, err := s.queue.GetStats()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if processing, _ := stats["processing"].(bool); processing {
		stats["status"] = "processing"
	} else {
		stats["status"] = "idle"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func (s *Server) handleSchedulerConfig(w http.ResponseWriter, r *http.Request) {
	if s.queue == nil {
		http.Error(w, `{"error":"upload queue not available"}`, http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := s.queue.Config()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":             cfg.Enabled,
			"process_time":        cfg.ProcessTime,
			"process_days":        cfg.ProcessDays,
			"max_concurrent":      cfg.MaxConcurrent,
			"max_per_batch":       cfg.MaxPerBatch,
			"process_screenshots": cfg.ProcessScreenshots,
			"process_documents":   cfg.ProcessDocuments,
			"process_photos":      cfg.ProcessPhotos,
			"process_videos":      cfg.ProcessVideos,
		})
	case http.MethodPost:
		cfg := s.queue.Config()
		var body struct {
			Enabled            *bool  `json:"enabled"`
			ProcessTime        string `json:"process_time"`
			ProcessDays        []int  `json:"process_days"`
			MaxConcurrent      *int   `json:"max_concurrent"`
			MaxPerBatch        *int   `json:"max_per_batch"`
			ProcessScreenshots *bool  `json:"process_screenshots"`
			ProcessDocuments   *bool  `json:"process_documents"`
			ProcessPhotos      *bool  `json:"process_photos"`
			ProcessVideos      *bool  `json:"process_videos"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		if body.Enabled != nil {
			cfg.Enabled = *body.Enabled
		}
		if body.ProcessTime != "" {
			cfg.ProcessTime = body.ProcessTime
		}
		if body.ProcessDays != nil {
			cfg.ProcessDays = body.ProcessDays
		}
		if body.MaxConcurrent != nil {
			cfg.MaxConcurrent = clampInt(*body.MaxConcurrent, 1, 4)
		}
		if body.MaxPerBatch != nil {
			cfg.MaxPerBatch = clampInt(*body.MaxPerBatch, 1, 500)
		}
		if body.ProcessScreenshots != nil {
			cfg.ProcessScreenshots = *body.ProcessScreenshots
		}
		if body.ProcessDocuments != nil {
			cfg.ProcessDocuments = *body.ProcessDocuments
		}
		if body.ProcessPhotos != nil {
			cfg.ProcessPhotos = *body.ProcessPhotos
		}
		if body.ProcessVideos != nil {
			cfg.ProcessVideos = *body.ProcessVideos
		}
		if err := s.queue.SaveConfig(cfg); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
	default:
		http.Error(w, `{"error":"GET or POST only"}`, http.StatusMethodNotAllowed)
	}
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
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
