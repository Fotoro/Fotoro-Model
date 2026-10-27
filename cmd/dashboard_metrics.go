package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const dashboardMetricsTTL = 45 * time.Second

// DashboardMetrics is a measured snapshot from the local catalogue and disks.
// SQLite keeps the last successful reading across server restarts.
type DashboardMetrics struct {
	Total            int    `json:"total"`
	Processed        int    `json:"processed"`
	Failed           int    `json:"failed"`
	ThumbnailsMedium int    `json:"thumbnails_medium"`
	StorageUsedBytes int64  `json:"storage_used_bytes"`
	DiskTotalBytes   uint64 `json:"disk_total_bytes"`
	DiskFreeBytes    uint64 `json:"disk_free_bytes"`
	DevicesCount     int    `json:"devices_count"`
	PhotosWithFaces  int    `json:"photos_with_faces"`
	QueuePending     int    `json:"queue_pending"`
	QueueFailed      int    `json:"queue_failed"`
	SearchVectors    int    `json:"search_vectors"`
	BackupVerified   int    `json:"backup_verified"`
	BackupPending    int    `json:"backup_pending"`
	BackupAvailable  bool   `json:"backup_available"`
	PrimaryAvailable bool   `json:"primary_available"`
	ConfiguredModel  string `json:"configured_model"`
	CollectedAt      string `json:"collected_at"`
	Stale            bool   `json:"stale,omitempty"`
}

func (s *Server) dashboardMetrics(ctx context.Context) (DashboardMetrics, error) {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()

	var stored string
	var collected int64
	err := s.db.QueryRowContext(ctx, `SELECT payload, collected_at FROM dashboard_metrics WHERE id = 1`).Scan(&stored, &collected)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DashboardMetrics{}, err
	}
	var previous DashboardMetrics
	if err == nil {
		if json.Unmarshal([]byte(stored), &previous) == nil && time.Since(time.Unix(collected, 0)) < dashboardMetricsTTL {
			return previous, nil
		}
	}

	current, collectErr := s.collectDashboardMetrics(ctx)
	if collectErr != nil {
		if previous.CollectedAt != "" {
			previous.Stale = true
			return previous, nil
		}
		return DashboardMetrics{}, collectErr
	}
	current.CollectedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(current)
	if err != nil {
		return DashboardMetrics{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO dashboard_metrics (id, payload, collected_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET payload = excluded.payload, collected_at = excluded.collected_at`, data, time.Now().Unix()); err != nil {
		return DashboardMetrics{}, err
	}
	return current, nil
}

func (s *Server) collectDashboardMetrics(ctx context.Context) (DashboardMetrics, error) {
	var m DashboardMetrics
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN preview_status = 'ready' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status IN ('missing', 'corrupt') OR preview_status = 'failed' THEN 1 ELSE 0 END), 0)
		FROM media_assets`).Scan(&m.Total, &m.Processed, &m.Failed); err != nil {
		return m, fmt.Errorf("media counts: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM images WHERE has_faces = 1`).Scan(&m.PhotosWithFaces); err != nil {
		return m, fmt.Errorf("face counts: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_devices WHERE is_active = 1`).Scan(&m.DevicesCount); err != nil {
		return m, fmt.Errorf("device counts: %w", err)
	}
	m.DevicesCount++ // Include this server.
	if err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status IN ('deferred', 'pending', 'processing') THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0)
		FROM analysis_jobs`).Scan(&m.QueuePending, &m.QueueFailed); err != nil {
		return m, fmt.Errorf("analysis queue: %w", err)
	}
	if s.queue != nil {
		if stats, err := s.queue.GetStats(); err == nil {
			pending, _ := stats["pending"].(int)
			failed, _ := stats["failed"].(int)
			m.QueuePending += pending
			m.QueueFailed += failed
		}
	}
	if s.index != nil {
		m.SearchVectors = s.index.Len()
	}
	m.ConfiguredModel = s.model

	baseDir := filepath.Dir(s.cacheDir)
	storageRoot := baseDir
	if s.media != nil {
		storageRoot = s.media.StorageRoot()
		m.PrimaryAvailable = s.media.PrimaryStatus().Available
		backup, err := s.media.BackupStatus()
		if err != nil {
			return m, fmt.Errorf("backup status: %w", err)
		}
		m.BackupAvailable = backup.Available
		m.BackupVerified = backup.Verified
		m.BackupPending = backup.Pending
	}
	originalBytes, _, err := scanDashboardFiles(storageRoot, false)
	if err != nil {
		return m, fmt.Errorf("storage usage: %w", err)
	}
	cacheBytes, mediumCount, err := scanDashboardFiles(s.cacheDir, true)
	if err != nil {
		return m, fmt.Errorf("cache usage: %w", err)
	}
	stagingBytes, _, err := scanDashboardFiles(filepath.Join(baseDir, "uploads"), false)
	if err != nil {
		return m, fmt.Errorf("staging usage: %w", err)
	}
	m.StorageUsedBytes = originalBytes + cacheBytes + stagingBytes
	m.ThumbnailsMedium = mediumCount
	if disk, err := statsDiskUsage(storageRoot); err == nil {
		m.DiskTotalBytes = disk.Total
		m.DiskFreeBytes = disk.Free
	}
	return m, nil
}

func scanDashboardFiles(root string, countMedium bool) (int64, int, error) {
	if root == "" {
		return 0, 0, nil
	}
	var bytes int64
	var medium int
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			bytes += info.Size()
			if countMedium && strings.HasPrefix(path, filepath.Join(root, "thumbnails", "medium")+string(os.PathSeparator)) && strings.HasSuffix(path, ".jpg") {
				medium++
			}
		}
		return nil
	})
	return bytes, medium, err
}
