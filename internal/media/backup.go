package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type BackupStatus struct {
	Configured bool   `json:"configured"`
	Available  bool   `json:"available"`
	Error      string `json:"error,omitempty"`
	Pending    int    `json:"pending"`
	Verified   int    `json:"verified"`
	Failed     int    `json:"failed"`
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func filesystemDevice(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("cannot identify storage device")
	}
	return uint64(st.Dev), nil
}

func (s *Service) primaryAvailable() error {
	device, err := s.deviceForPath(s.storageRoot)
	if err != nil {
		return fmt.Errorf("primary storage unavailable: %w", err)
	}
	if device != s.primaryDevice {
		return fmt.Errorf("primary storage mount changed")
	}
	return nil
}

func (s *Service) backupAvailable() error {
	if s.backupRoot == "" {
		return fmt.Errorf("secondary storage is not configured")
	}
	if err := s.primaryAvailable(); err != nil {
		return err
	}
	primary := s.primaryDevice
	var err error
	secondary, err := s.deviceForPath(s.backupRoot)
	if err != nil {
		return fmt.Errorf("secondary storage: %w", err)
	}
	if primary == secondary {
		return fmt.Errorf("secondary storage is on the primary filesystem")
	}
	if s.backupDevice != 0 && secondary != s.backupDevice {
		return fmt.Errorf("secondary storage mount changed")
	}
	return nil
}

func (s *Service) BackupStatus() (BackupStatus, error) {
	status := BackupStatus{Configured: s.backupRoot != ""}
	if err := s.backupAvailable(); err == nil {
		status.Available = true
	} else {
		status.Error = err.Error()
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM storage_copies WHERE storage_name = 'backup'
		AND status = 'verified' AND checksum_verified = 1`).Scan(&status.Verified); err != nil {
		return status, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM storage_copies WHERE storage_name = 'backup'
		AND status = 'failed'`).Scan(&status.Failed); err != nil {
		return status, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM media_assets a
		JOIN storage_copies p ON p.media_asset_id = a.id AND p.storage_name = 'primary'
			AND p.status = 'verified' AND p.checksum_verified = 1
		LEFT JOIN storage_copies b ON b.media_asset_id = a.id AND b.storage_name = 'backup'
			AND b.status = 'verified' AND b.checksum_verified = 1
		WHERE b.id IS NULL`).Scan(&status.Pending); err != nil {
		return status, err
	}
	return status, nil
}

// BackupPending copies a bounded batch. Pending work is reconstructed from
// durable media/copy rows after every restart.
func (s *Service) BackupPending(ctx context.Context, limit int) (int, error) {
	if err := s.backupAvailable(); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	rows, err := s.db.Query(`SELECT a.id, a.hash, a.storage_path, a.original_name
		FROM media_assets a
		JOIN storage_copies p ON p.media_asset_id = a.id AND p.storage_name = 'primary'
			AND p.status = 'verified' AND p.checksum_verified = 1
		LEFT JOIN storage_copies b ON b.media_asset_id = a.id AND b.storage_name = 'backup'
			AND b.status = 'verified' AND b.checksum_verified = 1
		WHERE b.id IS NULL ORDER BY a.imported_at, a.id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ id, hash, path, name string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.hash, &c.path, &c.name); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		if err := s.backupOne(c.id, c.hash, c.path, c.name); err != nil {
			return completed, fmt.Errorf("backup %s: %w", c.id, err)
		}
		completed++
	}
	return completed, nil
}

func (s *Service) backupOne(id, expectedHash, source, originalName string) error {
	if err := s.backupAvailable(); err != nil {
		return err
	}
	if !isSHA256(expectedHash) {
		return fmt.Errorf("invalid asset hash")
	}
	actual, err := hashFile(source)
	if err != nil || !strings.EqualFold(actual, expectedHash) {
		return fmt.Errorf("primary original failed checksum verification")
	}
	destination := filepath.Join(s.backupRoot, "originals", expectedHash[:2], expectedHash+safeExtension(originalName))
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return err
	}
	if existing, err := hashFile(destination); err == nil && strings.EqualFold(existing, expectedHash) {
		return s.markBackupVerified(id, destination)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp := destination + ".part"
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if actual, err := hashFile(temp); err != nil || !strings.EqualFold(actual, expectedHash) {
		return fmt.Errorf("secondary copy failed checksum verification")
	}
	if err := os.Rename(temp, destination); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return err
	}
	return s.markBackupVerified(id, destination)
}

func (s *Service) markBackupVerified(id, path string) error {
	_, err := s.db.Exec(`INSERT INTO storage_copies
		(media_asset_id, storage_name, storage_path, status, checksum_verified, verified_at)
		VALUES (?, 'backup', ?, 'verified', 1, ?)
		ON CONFLICT(media_asset_id, storage_name) DO UPDATE SET
			storage_path = excluded.storage_path, status = 'verified',
			checksum_verified = 1, verified_at = excluded.verified_at`, id, path, time.Now().UTC())
	return err
}
