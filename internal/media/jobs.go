package media

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Service) ResetInterruptedDerivativeJobs() error {
	_, err := s.db.Exec(`UPDATE analysis_jobs SET status = 'pending', updated_at = ?
		WHERE job_type = 'thumbnail' AND status = 'processing'`, time.Now().UTC())
	return err
}

// ProcessDerivativeJobs is independent of durable upload completion. A missing
// model cannot prevent thumbnails or original downloads.
func (s *Service) ProcessDerivativeJobs(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 20 {
		limit = 5
	}
	s.derivativeMu.Lock()
	defer s.derivativeMu.Unlock()
	completed := 0
	for i := 0; i < limit; i++ {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		var id, path, mimeType, name string
		err := s.db.QueryRow(`SELECT a.id, a.storage_path, a.mime_type, a.original_name
			FROM analysis_jobs j JOIN media_assets a ON a.id = j.media_asset_id
			WHERE j.job_type = 'thumbnail' AND j.status = 'pending'
			ORDER BY j.created_at, j.id LIMIT 1`).Scan(&id, &path, &mimeType, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return completed, nil
		}
		if err != nil {
			return completed, err
		}
		now := time.Now().UTC()
		if _, err := s.db.Exec(`UPDATE analysis_jobs SET status = 'processing', updated_at = ?
			WHERE media_asset_id = ? AND job_type = 'thumbnail'`, now, id); err != nil {
			return completed, err
		}
		if err := s.generateImageDerivatives(id, path, mimeType, name, now); err != nil {
			if _, updateErr := s.db.Exec(`UPDATE analysis_jobs SET status = 'failed',
				error_message = ?, updated_at = ? WHERE media_asset_id = ? AND job_type = 'thumbnail'`,
				err.Error(), time.Now().UTC(), id); updateErr != nil {
				return completed, updateErr
			}
			continue
		}
		if _, err := s.db.Exec(`UPDATE analysis_jobs SET status = 'done', error_message = NULL,
			updated_at = ? WHERE media_asset_id = ? AND job_type = 'thumbnail'`, time.Now().UTC(), id); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}
