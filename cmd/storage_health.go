package cmd

import (
	"net/http"
	"time"
)

// handleStorageHealth is the consolidated operator health payload: disk
// headroom, backup backlog and its age, upload queue depth, catalogue
// integrity, rehydrated cache, and the tier ladder.
func (s *Server) handleStorageHealth(w http.ResponseWriter, r *http.Request) {
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
	tiers, err := s.media.TierStats()
	if err != nil {
		http.Error(w, `{"error":"tier stats unavailable"}`, http.StatusInternalServerError)
		return
	}
	backup, err := s.media.BackupStatus()
	if err != nil {
		http.Error(w, `{"error":"storage status unavailable"}`, http.StatusInternalServerError)
		return
	}
	backlog, err := s.media.BackupBacklog()
	if err != nil {
		http.Error(w, `{"error":"backlog stats unavailable"}`, http.StatusInternalServerError)
		return
	}
	primary := s.media.PrimaryStatus()
	uploads := map[string]interface{}{"pending": 0, "processing": false}
	if s.queue != nil {
		if stats, qerr := s.queue.GetStats(); qerr == nil {
			uploads["pending"] = stats["pending"]
			uploads["processing"] = stats["processing"]
			uploads["failed"] = stats["failed"]
		}
	}
	var openSessions int
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM upload_sessions WHERE status = 'uploading'`).Scan(&openSessions); err == nil {
		uploads["open_sessions"] = openSessions
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"collected_at": time.Now().UTC().Format(time.RFC3339),
		"database": map[string]interface{}{
			"schema_version": version,
			"quick_check":    integrity,
		},
		"primary": map[string]interface{}{
			"available":   primary.Available,
			"error":       primary.Error,
			"free_bytes":  tiers.PrimaryFree,
			"total_bytes": tiers.PrimaryTotal,
		},
		"backup": map[string]interface{}{
			"configured":             backup.Configured,
			"available":              backup.Available,
			"error":                  backup.Error,
			"pending":                backlog.Pending,
			"oldest_pending_seconds": backlog.OldestPendingSeconds,
			"verified":               backup.Verified,
			"failed":                 backup.Failed,
			"unverified_primary":     backup.Unverified,
		},
		"upload_queue": uploads,
		"tiers":        tiers,
	})
}
