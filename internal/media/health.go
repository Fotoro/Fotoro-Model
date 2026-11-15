package media

import (
	"database/sql"
	"errors"
	"time"
)

// BackupBacklog describes assets with a verified primary copy that still lack
// a verified copy on secondary storage. OldestPendingSeconds is -1 when the
// queue is empty. It shares the pending predicate with BackupStatus so the
// counts always agree.
type BackupBacklog struct {
	Pending              int   `json:"pending"`
	OldestPendingSeconds int64 `json:"oldest_pending_seconds"`
}

const backlogPendingJoins = `FROM media_assets a
	JOIN storage_copies p ON p.media_asset_id = a.id AND p.storage_name = 'primary'
		AND p.status = 'verified' AND p.checksum_verified = 1
	LEFT JOIN storage_copies b ON b.media_asset_id = a.id AND b.storage_name = 'backup'
		AND b.status = 'verified' AND b.checksum_verified = 1
	WHERE b.id IS NULL`

func (s *Service) BackupBacklog() (*BackupBacklog, error) {
	b := &BackupBacklog{OldestPendingSeconds: -1}
	if err := s.db.QueryRow(`SELECT COUNT(*) ` + backlogPendingJoins).Scan(&b.Pending); err != nil {
		return nil, err
	}
	var oldest time.Time
	err := s.db.QueryRow(`SELECT a.imported_at ` + backlogPendingJoins +
		` ORDER BY a.imported_at ASC LIMIT 1`).Scan(&oldest)
	if errors.Is(err, sql.ErrNoRows) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if age := int64(time.Since(oldest).Seconds()); age > 0 {
		b.OldestPendingSeconds = age
	} else {
		b.OldestPendingSeconds = 0
	}
	return b, nil
}
