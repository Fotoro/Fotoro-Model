package media

import (
	"context"
	"os"
)

// ReconcileMediaSizes repairs catalogue rows imported from the legacy images
// table, which had no file-size column. Originals are only statted when their
// recorded size is missing, so dashboard refreshes never walk the library.
func (s *Service) ReconcileMediaSizes(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		limit = 100000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, storage_path FROM media_assets
		WHERE file_size <= 0 ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ id, path string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.path); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	type measured struct {
		id   string
		size int64
	}
	var updates []measured
	for _, item := range candidates {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		info, err := os.Stat(item.path)
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			updates = append(updates, measured{item.id, info.Size()})
		}
	}
	if len(updates) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `UPDATE media_assets SET file_size = ? WHERE id = ? AND file_size <= 0`)
	if err != nil {
		return 0, err
	}
	for _, item := range updates {
		if _, err := stmt.ExecContext(ctx, item.size, item.id); err != nil {
			stmt.Close()
			return 0, err
		}
	}
	if err := stmt.Close(); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(updates), nil
}
