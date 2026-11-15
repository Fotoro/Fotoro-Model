package cmd

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func registerStorageRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("/api/v1/storage", s.withAuth(s.handleStorageStatus))
	mux.HandleFunc("/api/v1/storage/sync", s.withAuth(s.handleStorageSync))
	mux.HandleFunc("/api/v1/storage/verify", s.withAuth(s.handleStorageVerify))
	mux.HandleFunc("/api/v1/storage/tiers", s.withAuth(s.handleStorageTiers))
	mux.HandleFunc("/api/v1/storage/tier/run", s.withAuth(s.handleTierRun))
	mux.HandleFunc("/api/v1/storage/health", s.withAuth(s.handleStorageHealth))
	mux.HandleFunc("/api/v1/storage/catalogue-snapshot", s.withAuth(s.handleCatalogueSnapshot))
	mux.HandleFunc("/api/v1/embed/status", s.withAuth(s.handleEmbedStatus))
	mux.HandleFunc("/api/v1/embed/backfill", s.withAuth(s.handleEmbedBackfill))
}

func (s *Server) handleEmbedStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	pending, err := EmbedPending(s.db.DB, s.embed)
	if err != nil {
		http.Error(w, `{"error":"embed status unavailable"}`, http.StatusInternalServerError)
		return
	}
	var total, embedded int
	s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN embedding IS NOT NULL THEN 1 ELSE 0 END),0) FROM images`).Scan(&total, &embedded)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"model":           s.embed.Model(),
		"dim":             s.embed.Dim(),
		"total_images":    total,
		"embedded":        embedded,
		"pending":         pending,
		"index_vectors":   s.index.Len(),
		"backend_healthy": s.embed.HealthCheck() == nil,
	})
}

func (s *Server) handleEmbedBackfill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	if err := s.embed.HealthCheck(); err != nil {
		http.Error(w, `{"error":"embed backend unreachable"}`, http.StatusServiceUnavailable)
		return
	}
	done, skipped, err := BackfillEmbeddings(s.db.DB, s.embed, 200)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	pending, _ := EmbedPending(s.db.DB, s.embed)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"embedded": done, "skipped": skipped, "pending": pending,
	})
}

func (s *Server) handleStorageTiers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	stats, err := s.media.TierStats()
	if err != nil {
		http.Error(w, `{"error":"tier stats unavailable"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleTierRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	res, err := s.media.RunTierPass(25)
	if err != nil {
		http.Error(w, `{"error":"tier pass failed"}`, http.StatusInternalServerError)
		return
	}
	packed, err := s.media.PackWarmAssets(100)
	if err != nil {
		http.Error(w, `{"error":"pack pass failed"}`, http.StatusInternalServerError)
		return
	}
	res.Packed = append(res.Packed, packed.Packed...)
	res.Errors = append(res.Errors, packed.Errors...)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCatalogueSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	path, err := s.media.SnapshotCatalogue(s.dbPath(), 7)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"snapshot": path})
}

func (s *Server) handleStorageVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	checked, err := s.media.VerifyBackupCopies(ctx, 25)
	if err != nil {
		http.Error(w, `{"error":"verification unavailable or failed"}`, http.StatusServiceUnavailable)
		return
	}
	status, err := s.media.BackupStatus()
	if err != nil {
		http.Error(w, `{"error":"storage status unavailable"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"checked": checked, "storage": status})
}

func (s *Server) handleStorageStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	status, err := s.media.BackupStatus()
	if err != nil {
		http.Error(w, `{"error":"storage status unavailable"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleStorageSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	primary, err := s.media.VerifyPrimaryPending(ctx, 10)
	if err != nil {
		http.Error(w, `{"error":"primary verification failed"}`, http.StatusServiceUnavailable)
		return
	}
	count, err := s.media.BackupPending(ctx, 10)
	if err != nil {
		http.Error(w, `{"error":"backup unavailable or failed"}`, http.StatusServiceUnavailable)
		return
	}
	status, err := s.media.BackupStatus()
	if err != nil {
		http.Error(w, `{"error":"storage status unavailable"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"primary_verified": primary, "copied": count, "storage": status})
}

func (s *Server) startBackupWorker() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		tierTicker := time.NewTicker(15 * time.Minute)
		verifyTicker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		defer tierTicker.Stop()
		defer verifyTicker.Stop()
		for {
			select {
			case <-ticker.C:
				status, err := s.media.BackupStatus()
				if err != nil || !status.Available {
					continue
				}
				s.backgroundActive.Add(1)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
				_, _ = s.media.VerifyPrimaryPending(ctx, 10)
				_, _ = s.media.BackupPending(ctx, 10)
				_, _ = s.media.BackupPacks(ctx, 1)
				cancel()
				s.backgroundActive.Add(-1)
			case <-tierTicker.C:
				s.backgroundActive.Add(1)
				_, _ = s.media.RunTierPass(25)
				_, _ = s.media.PackWarmAssets(100)
				_, _ = s.media.CompactPacks(1)
				if s.embed.HealthCheck() == nil {
					done, _, err := BackfillEmbeddings(s.db.DB, s.embed, 50)
					if err == nil && done > 0 {
						if n, lerr := s.index.LoadNew(s.db.DB); lerr == nil && n > 0 {
							fmt.Printf("[EMBED] %d new, %d vectors in index\n", done, s.index.Len())
						}
					}
				}
				s.backgroundActive.Add(-1)
			case <-verifyTicker.C:
				s.backgroundActive.Add(1)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
				_, _ = s.media.VerifyBackupCopies(ctx, 100)
				_, _ = s.media.SnapshotCatalogue(s.dbPath(), 7)
				cancel()
				s.backgroundActive.Add(-1)
			}
		}
	}()
}
