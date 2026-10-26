package cmd

import (
	"net/http"
	"os"
)

func registerDiagnosticsRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/ready", s.handleReady)
	mux.HandleFunc("/api/v1/diagnostics", s.withAuth(s.handleDiagnostics))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	if err := s.db.PingContext(r.Context()); err != nil || !s.media.PrimaryStatus().Available {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	var version int
	if err := s.db.QueryRowContext(r.Context(), `PRAGMA user_version`).Scan(&version); err != nil {
		http.Error(w, `{"error":"database unavailable","code":"DB_UNAVAILABLE"}`, http.StatusServiceUnavailable)
		return
	}
	var integrity string
	if err := s.db.QueryRowContext(r.Context(), `PRAGMA quick_check`).Scan(&integrity); err != nil {
		integrity = "unavailable"
	}
	backup, err := s.media.BackupStatus()
	if err != nil {
		http.Error(w, `{"error":"storage status unavailable","code":"STORAGE_UNAVAILABLE"}`, http.StatusServiceUnavailable)
		return
	}
	var queue interface{}
	if s.queue != nil {
		queue, _ = s.queue.GetStats()
	}
	vectors := 0
	if s.index != nil {
		vectors = s.index.Len()
	}
	tailscale := map[string]interface{}{"running": false, "funnel_enabled": false}
	if s.tailscale != nil {
		tailscale["running"] = s.tailscale.IsRunning()
		tailscale["funnel_enabled"] = s.tailscale.FunnelConfigured()
	}
	tailscale["private_site_requested"] = os.Getenv("FOTORO_PRIVATE_SITE") == "1"
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"database":  map[string]interface{}{"schema_version": version, "quick_check": integrity},
		"primary":   s.media.PrimaryStatus(),
		"backup":    backup,
		"queue":     queue,
		"search":    map[string]interface{}{"model": s.model, "vectors": vectors},
		"tailscale": tailscale,
	})
}
