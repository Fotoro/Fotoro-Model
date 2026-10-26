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
	Unverified int    `json:"unverified_primary"`
	Missing    int    `json:"missing_primary"`
	Corrupt    int    `json:"corrupt_primary"`
	Pending    int    `json:"pending"`
	Verified   int    `json:"verified"`
	Failed     int    `json:"failed"`
}

type PrimaryStatus struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

func (s *Service) PrimaryStatus() PrimaryStatus {
	if err := s.primaryAvailable(); err != nil {
		return PrimaryStatus{Error: err.Error()}
	}
	return PrimaryStatus{Available: true}
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

func validateMount(storageRoot, mountDir string, deviceForPath func(string) (uint64, error)) error {
	root, err := filepath.Abs(storageRoot)
	if err != nil {
		return err
	}
	mount, err := filepath.Abs(mountDir)
	if err != nil {
		return err
	}
	if mount == string(filepath.Separator) {
		return fmt.Errorf("filesystem root is not a dedicated storage mount")
	}
	rel, err := filepath.Rel(mount, root)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("storage directory is outside its configured mount")
	}
	mountedDevice, err := deviceForPath(mount)
	if err != nil {
		return err
	}
	parentDevice, err := deviceForPath(filepath.Dir(mount))
	if err != nil {
		return err
	}
	if mountedDevice == parentDevice {
		return fmt.Errorf("configured storage mount is not mounted")
	}
	rootDevice, err := deviceForPath(root)
	if err != nil {
		return err
	}
	if rootDevice != mountedDevice {
		return fmt.Errorf("storage directory is on a different filesystem than its mount")
	}
	return nil
}

func (s *Service) primaryAvailable() error {
	if s.primaryMount != "" {
		if err := validateMount(s.storageRoot, s.primaryMount, s.deviceForPath); err != nil {
			return fmt.Errorf("primary mount: %w", err)
		}
	}
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
	if err := validateMount(s.backupRoot, s.backupMount, s.deviceForPath); err != nil {
		return fmt.Errorf("secondary mount: %w", err)
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
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM media_assets a
		LEFT JOIN storage_copies p ON p.media_asset_id = a.id AND p.storage_name = 'primary'
			AND p.status = 'verified' AND p.checksum_verified = 1
		WHERE p.id IS NULL`).Scan(&status.Unverified); err != nil {
		return status, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM media_assets WHERE status = 'missing'`).Scan(&status.Missing); err != nil {
		return status, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM media_assets WHERE status = 'corrupt'`).Scan(&status.Corrupt); err != nil {
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

// VerifyPrimaryPending reconciles legacy catalogue rows before they enter the
// backup queue. An old "verified" flag alone is never accepted as proof.
func (s *Service) VerifyPrimaryPending(ctx context.Context, limit int) (int, error) {
	if err := s.primaryAvailable(); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	rows, err := s.db.Query(`SELECT a.id, a.hash, a.storage_path FROM media_assets a
		LEFT JOIN storage_copies p ON p.media_asset_id = a.id AND p.storage_name = 'primary'
			AND p.status = 'verified' AND p.checksum_verified = 1
		WHERE p.id IS NULL AND a.status != 'corrupt'
		ORDER BY CASE WHEN a.status = 'missing' THEN 1 ELSE 0 END,
			a.imported_at, a.id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ id, hash, path string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.hash, &c.path); err != nil {
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
	verified := 0
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return verified, err
		}
		device, err := s.deviceForPath(filepath.Dir(c.path))
		if err != nil || device != s.primaryDevice {
			_, _ = s.db.Exec(`UPDATE media_assets SET status = 'missing', checksum_verified = 0,
				updated_at = ? WHERE id = ?`, time.Now().UTC(), c.id)
			continue
		}
		actual, err := hashFile(c.path)
		if err != nil {
			_, _ = s.db.Exec(`UPDATE media_assets SET status = 'missing', checksum_verified = 0,
				updated_at = ? WHERE id = ?`, time.Now().UTC(), c.id)
			continue
		}
		if !isSHA256(c.hash) || !strings.EqualFold(actual, c.hash) {
			_, _ = s.db.Exec(`UPDATE media_assets SET status = 'corrupt', checksum_verified = 0,
				updated_at = ? WHERE id = ?`, time.Now().UTC(), c.id)
			continue
		}
		info, err := os.Stat(c.path)
		if err != nil {
			continue
		}
		now := time.Now().UTC()
		tx, err := s.db.Begin()
		if err != nil {
			return verified, err
		}
		_, err = tx.Exec(`UPDATE media_assets SET file_size = ?, checksum_verified = 1,
			status = 'verified', updated_at = ? WHERE id = ?`, info.Size(), now, c.id)
		if err == nil {
			_, err = tx.Exec(`INSERT INTO storage_copies
				(media_asset_id, storage_name, storage_path, status, checksum_verified, verified_at)
				VALUES (?, 'primary', ?, 'verified', 1, ?)
				ON CONFLICT(media_asset_id, storage_name) DO UPDATE SET
				storage_path = excluded.storage_path, status = 'verified',
				checksum_verified = 1, verified_at = excluded.verified_at`, c.id, c.path, now)
		}
		if err != nil {
			tx.Rollback()
			return verified, err
		}
		if err := tx.Commit(); err != nil {
			return verified, err
		}
		verified++
	}
	return verified, nil
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
			_, _ = s.db.Exec(`INSERT INTO storage_copies
				(media_asset_id, storage_name, storage_path, status, checksum_verified)
				VALUES (?, 'backup', '', 'failed', 0)
				ON CONFLICT(media_asset_id, storage_name) DO UPDATE SET
				status = 'failed', checksum_verified = 0`, c.id)
			return completed, fmt.Errorf("backup %s: %w", c.id, err)
		}
		completed++
	}
	return completed, nil
}

// VerifyBackupCopies walks the oldest verified copies first. A failed copy
// becomes pending again and the next backup pass repairs it from the primary.
func (s *Service) VerifyBackupCopies(ctx context.Context, limit int) (int, error) {
	if err := s.backupAvailable(); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 100 {
		limit = 25
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	rows, err := s.db.Query(`SELECT b.media_asset_id, a.hash, b.storage_path
		FROM storage_copies b JOIN media_assets a ON a.id = b.media_asset_id
		WHERE b.storage_name = 'backup' AND b.status = 'verified' AND b.checksum_verified = 1
		ORDER BY b.verified_at, b.media_asset_id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type copyRecord struct{ id, hash, path string }
	var copies []copyRecord
	for rows.Next() {
		var record copyRecord
		if err := rows.Scan(&record.id, &record.hash, &record.path); err != nil {
			rows.Close()
			return 0, err
		}
		copies = append(copies, record)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	checked := 0
	for _, record := range copies {
		if err := ctx.Err(); err != nil {
			return checked, err
		}
		actual, err := hashFile(record.path)
		if err != nil || !strings.EqualFold(actual, record.hash) {
			_, err = s.db.Exec(`UPDATE storage_copies SET status = 'failed', checksum_verified = 0,
				verified_at = NULL WHERE media_asset_id = ? AND storage_name = 'backup'`, record.id)
		} else {
			_, err = s.db.Exec(`UPDATE storage_copies SET verified_at = ?
				WHERE media_asset_id = ? AND storage_name = 'backup'`, time.Now().UTC(), record.id)
		}
		if err != nil {
			return checked, err
		}
		checked++
	}
	return checked, nil
}

func (s *Service) backupOne(id, expectedHash, source, originalName string) error {
	if err := s.backupAvailable(); err != nil {
		return err
	}
	if !isSHA256(expectedHash) {
		return fmt.Errorf("invalid asset hash")
	}
	device, err := s.deviceForPath(filepath.Dir(source))
	if err != nil || device != s.primaryDevice {
		return fmt.Errorf("primary original is not on the configured primary filesystem")
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
