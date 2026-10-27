package media

import (
	"context"
	"fmt"
	"time"

	"fotoro/internal/validate"
)

// BackfillLargePreviews adds a disposable 2048 px viewing tier to existing
// photos. Work is bounded by limit and originals are verified, then read only.
func (s *Service) BackfillLargePreviews(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 25
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.storage_path FROM media_assets a
		WHERE a.media_kind = 'image' AND a.status = 'verified' AND a.preview_status = 'ready'
		AND NOT EXISTS (SELECT 1 FROM derivatives d
			WHERE d.media_asset_id = a.id AND d.kind = 'thumbnail_large')
		ORDER BY a.file_size DESC, a.id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type source struct{ id, path string }
	items := make([]source, 0, limit)
	for rows.Next() {
		var item source
		if err := rows.Scan(&item.id, &item.path); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		actualHash, err := hashFile(item.path)
		if err != nil {
			return completed, fmt.Errorf("original %s failed verification: %w", item.id[:8], err)
		}
		if actualHash != item.id {
			return completed, fmt.Errorf("original %s failed checksum verification", item.id[:8])
		}
		path, size, err := validate.SaveLargeThumbnail(s.cacheDir, item.id, item.path)
		if err != nil {
			return completed, fmt.Errorf("large preview %s: %w", item.id[:8], err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO derivatives
			(media_asset_id, kind, storage_path, mime_type, file_size, created_at)
			VALUES (?, 'thumbnail_large', ?, 'image/jpeg', ?, ?)
			ON CONFLICT(media_asset_id, kind) DO UPDATE SET
				storage_path = excluded.storage_path, file_size = excluded.file_size`,
			item.id, path, size, time.Now().UTC()); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}
