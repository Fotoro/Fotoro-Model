package cmd

import (
	"context"
	"net/http"
	"time"
)

func registerStorageRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("/api/v1/storage", s.withAuth(s.handleStorageStatus))
	mux.HandleFunc("/api/v1/storage/sync", s.withAuth(s.handleStorageSync))
	mux.HandleFunc("/api/v1/storage/verify", s.withAuth(s.handleStorageVerify))
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
		verifyTicker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		defer verifyTicker.Stop()
		for {
			select {
			case <-ticker.C:
				status, err := s.media.BackupStatus()
				if err != nil || !status.Available {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
				_, _ = s.media.VerifyPrimaryPending(ctx, 10)
				_, _ = s.media.BackupPending(ctx, 10)
				cancel()
			case <-verifyTicker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
				_, _ = s.media.VerifyBackupCopies(ctx, 100)
				cancel()
			}
		}
	}()
}
