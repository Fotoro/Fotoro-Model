package media

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/validate"
)

const (
	DefaultMaxAssetSize = int64(100 * 1024 * 1024 * 1024) // 100 GiB
	MaxChunkSize        = int64(16 * 1024 * 1024)         // 16 MiB
)

var (
	ErrNotFound      = errors.New("not found")
	ErrForbidden     = errors.New("forbidden")
	ErrOffset        = errors.New("upload offset mismatch")
	ErrUploadExpired = errors.New("upload session expired")
)

type Service struct {
	db            *db.DB
	storageRoot   string
	backupRoot    string
	primaryDevice uint64
	backupDevice  uint64
	deviceForPath func(string) (uint64, error)
	cacheDir      string
	maxSize       int64
	uploadLocks   sync.Map
	backupMu      sync.Mutex
}

type Asset struct {
	ID               string     `json:"id"`
	Hash             string     `json:"sha256"`
	OriginalName     string     `json:"original_name"`
	MediaKind        string     `json:"media_kind"`
	MIMEType         string     `json:"mime_type"`
	FileSize         int64      `json:"file_size"`
	Status           string     `json:"status"`
	CapturedAt       *time.Time `json:"captured_at,omitempty"`
	ImportedAt       time.Time  `json:"imported_at"`
	Width            *int       `json:"width,omitempty"`
	Height           *int       `json:"height,omitempty"`
	DurationMS       *int64     `json:"duration_ms,omitempty"`
	ChecksumVerified bool       `json:"checksum_verified"`
	PreviewStatus    string     `json:"preview_status"`
	SourceDeviceID   string     `json:"source_device_id,omitempty"`
	DeviceAssetID    string     `json:"device_asset_id,omitempty"`
	ContentURL       string     `json:"content_url"`
	ThumbnailURL     string     `json:"thumbnail_url,omitempty"`
	SafeToDelete     bool       `json:"safe_to_delete"`
	StorageCopies    int        `json:"verified_storage_copies"`
}

type ImportOptions struct {
	OriginalName  string
	MIMEType      string
	DeviceID      string
	DeviceAssetID string
	MoveSource    bool
}

type ImportResult struct {
	Asset     *Asset `json:"asset"`
	Duplicate bool   `json:"duplicate"`
}

type UploadSession struct {
	ID             string    `json:"id"`
	OwnerID        string    `json:"-"`
	DeviceID       string    `json:"device_id,omitempty"`
	DeviceAssetID  string    `json:"device_asset_id,omitempty"`
	OriginalName   string    `json:"original_name"`
	MIMEType       string    `json:"mime_type"`
	ExpectedSize   int64     `json:"expected_size"`
	ExpectedSHA256 string    `json:"expected_sha256,omitempty"`
	ReceivedSize   int64     `json:"received_size"`
	TempPath       string    `json:"-"`
	Status         string    `json:"status"`
	MediaAssetID   string    `json:"media_asset_id,omitempty"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type CreateUploadRequest struct {
	OriginalName   string `json:"original_name"`
	MIMEType       string `json:"mime_type"`
	ExpectedSize   int64  `json:"size"`
	ExpectedSHA256 string `json:"sha256"`
	DeviceID       string `json:"device_id"`
	DeviceAssetID  string `json:"device_asset_id"`
}

func NewService(database *db.DB, dbPath string) (*Service, error) {
	if database == nil {
		return nil, fmt.Errorf("media service requires database")
	}
	if strings.TrimSpace(dbPath) == "" {
		dbPath = "fotoro.db"
	}
	baseDir := filepath.Dir(dbPath)
	storageRoot := strings.TrimSpace(os.Getenv("FOTORO_STORAGE_DIR"))
	if storageRoot == "" {
		storageRoot = filepath.Join(baseDir, "library")
		if err := os.MkdirAll(storageRoot, 0750); err != nil {
			return nil, err
		}
	} else if !filepath.IsAbs(storageRoot) {
		storageRoot = filepath.Join(baseDir, storageRoot)
	}
	primaryDevice, err := filesystemDevice(storageRoot)
	if err != nil {
		return nil, fmt.Errorf("primary storage must exist before server startup: %w", err)
	}
	backupRoot := strings.TrimSpace(os.Getenv("FOTORO_BACKUP_DIR"))
	var backupDevice uint64
	if backupRoot != "" {
		if !filepath.IsAbs(backupRoot) {
			return nil, fmt.Errorf("FOTORO_BACKUP_DIR must be an absolute path")
		}
		backupDevice, err = filesystemDevice(backupRoot)
		if err != nil {
			return nil, fmt.Errorf("secondary storage must exist before server startup: %w", err)
		}
		if backupDevice == primaryDevice {
			return nil, fmt.Errorf("secondary storage must use a different filesystem")
		}
	}
	maxSize := DefaultMaxAssetSize
	if raw := strings.TrimSpace(os.Getenv("FOTORO_MAX_ASSET_BYTES")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			maxSize = n
		}
	}
	s := &Service{
		db:            database,
		storageRoot:   storageRoot,
		backupRoot:    backupRoot,
		primaryDevice: primaryDevice,
		backupDevice:  backupDevice,
		deviceForPath: filesystemDevice,
		cacheDir:      filepath.Join(baseDir, ".cache"),
		maxSize:       maxSize,
	}
	for _, dir := range []string{
		filepath.Join(storageRoot, "originals"),
		filepath.Join(storageRoot, ".partial"),
		filepath.Join(s.cacheDir, "thumbnails", "small"),
		filepath.Join(s.cacheDir, "thumbnails", "medium"),
	} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Service) StorageRoot() string { return s.storageRoot }

func (s *Service) ImportFile(sourcePath string, opts ImportOptions) (*ImportResult, error) {
	if err := s.primaryAvailable(); err != nil {
		return nil, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source is not a regular file")
	}
	if info.Size() < 0 || info.Size() > s.maxSize {
		return nil, fmt.Errorf("asset size exceeds configured limit")
	}

	hash, err := hashFile(sourcePath)
	if err != nil {
		return nil, err
	}
	if existing, err := s.GetAsset(hash); err == nil {
		storedPath, _, pathErr := s.ContentPath(existing.ID)
		if pathErr != nil {
			return nil, pathErr
		}
		if storedHash, hashErr := hashFile(storedPath); hashErr != nil || storedHash != hash {
			return nil, fmt.Errorf("existing original failed verification")
		}
		if _, err := s.db.Exec(`INSERT INTO storage_copies
			(media_asset_id, storage_name, storage_path, status, checksum_verified, verified_at)
			VALUES (?, 'primary', ?, 'verified', 1, ?)
			ON CONFLICT(media_asset_id, storage_name) DO UPDATE SET
			storage_path = excluded.storage_path, status = 'verified',
			checksum_verified = 1, verified_at = excluded.verified_at`,
			existing.ID, storedPath, time.Now().UTC()); err != nil {
			return nil, err
		}
		if err := s.linkDeviceAsset(existing.ID, opts, info.Size(), hash); err != nil {
			return nil, err
		}
		existing, err = s.GetAsset(existing.ID)
		if err != nil {
			return nil, err
		}
		return &ImportResult{Asset: existing, Duplicate: true}, nil
	}

	name := strings.TrimSpace(opts.OriginalName)
	if name == "" {
		name = filepath.Base(sourcePath)
	}
	mimeType := detectMIMEType(sourcePath, name, opts.MIMEType)
	kind := mediaKind(mimeType, name)
	destination := s.assetStoragePath(hash, name)
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return nil, err
	}
	// Keep staging intact until the asset and upload session are committed.
	created, err := materializeOriginal(sourcePath, destination, false)
	if err != nil {
		return nil, err
	}
	if storedHash, hashErr := hashFile(destination); hashErr != nil || storedHash != hash {
		if created {
			_ = os.Remove(destination)
		}
		return nil, fmt.Errorf("committed original failed verification")
	}

	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		if created {
			_ = os.Remove(destination)
		}
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		INSERT INTO media_assets (
			id, hash, original_name, storage_path, media_kind, mime_type, file_size,
			status, imported_at, checksum_verified, preview_status,
			source_device_id, device_asset_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'verified', ?, 1, 'pending', ?, ?, ?, ?)
	`, hash, hash, name, destination, kind, mimeType, info.Size(), now,
		nullIfEmpty(opts.DeviceID), nullIfEmpty(opts.DeviceAssetID), now, now)
	if err != nil {
		if created {
			_ = os.Remove(destination)
		}
		return nil, err
	}

	_, err = tx.Exec(`
		INSERT OR REPLACE INTO storage_copies (
			media_asset_id, storage_name, storage_path, status,
			checksum_verified, verified_at, created_at
		) VALUES (?, 'primary', ?, 'verified', 1, ?, ?)
	`, hash, destination, now, now)
	if err != nil {
		if created {
			_ = os.Remove(destination)
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if created {
			_ = os.Remove(destination)
		}
		return nil, err
	}
	if err := s.linkDeviceAsset(hash, opts, info.Size(), hash); err != nil {
		return nil, err
	}

	if kind == "image" {
		s.generateImageDerivatives(hash, destination, mimeType, name, now)
	} else {
		_, _ = s.db.Exec(`UPDATE media_assets SET preview_status = 'unavailable', updated_at = ? WHERE id = ?`, now, hash)
	}

	asset, err := s.GetAsset(hash)
	if err != nil {
		return nil, err
	}
	return &ImportResult{Asset: asset}, nil
}

func (s *Service) generateImageDerivatives(id, path, mimeType, name string, now time.Time) {
	meta, err := validate.PrepareImage(path)
	if err != nil {
		_, _ = s.db.Exec(`UPDATE media_assets SET preview_status = 'unavailable', updated_at = ? WHERE id = ?`, now, id)
		syncLegacyImage(s.db, id, path, mimeType, name, nil, now)
		return
	}
	if err := validate.SaveThumbnails(s.cacheDir, meta); err != nil {
		_, _ = s.db.Exec(`UPDATE media_assets SET preview_status = 'failed', updated_at = ? WHERE id = ?`, now, id)
		syncLegacyImage(s.db, id, path, mimeType, name, meta, now)
		return
	}
	var captured interface{}
	if !meta.TakenAt.IsZero() {
		captured = meta.TakenAt.UTC()
	}
	_, _ = s.db.Exec(`
		UPDATE media_assets SET width = ?, height = ?, captured_at = ?,
			preview_status = 'ready', updated_at = ? WHERE id = ?
	`, meta.Width, meta.Height, captured, now, id)
	for _, size := range []string{"small", "medium"} {
		thumbPath := filepath.Join(s.cacheDir, "thumbnails", size, id[:2], id+".jpg")
		thumbInfo, err := os.Stat(thumbPath)
		if err != nil {
			continue
		}
		_, _ = s.db.Exec(`
			INSERT OR REPLACE INTO derivatives (
				media_asset_id, kind, storage_path, mime_type, file_size, created_at
			) VALUES (?, ?, ?, 'image/jpeg', ?, ?)
		`, id, "thumbnail_"+size, thumbPath, thumbInfo.Size(), now)
	}
	syncLegacyImage(s.db, id, path, mimeType, name, meta, now)
}

func syncLegacyImage(database *db.DB, hash, path, mimeType, name string, meta *validate.ImageMeta, now time.Time) {
	var captured interface{}
	if meta != nil && !meta.TakenAt.IsZero() {
		captured = meta.TakenAt.UTC()
	}
	_, _ = database.Exec(`
		INSERT OR IGNORE INTO images (
			path, hash, caption, category, tags, has_text, has_faces,
			orientation, tier, processed_at, taken_at, content_type, phash
		) VALUES (?, ?, '', 'unprocessed', '', 0, 0, 'unknown', 'offload', ?, ?, ?, ?)
	`, path, hash, now, captured, mimeType, func() string {
		if meta != nil {
			return meta.PHash
		}
		return ""
	}())
}

func (s *Service) linkDeviceAsset(assetID string, opts ImportOptions, size int64, hash string) error {
	if strings.TrimSpace(opts.DeviceID) == "" || strings.TrimSpace(opts.DeviceAssetID) == "" {
		return nil
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO device_assets (
			device_id, device_asset_id, media_asset_id, original_name,
			file_size, sha256, sync_status, verified_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 'verified', ?, ?, ?)
		ON CONFLICT(device_id, device_asset_id) DO UPDATE SET
			media_asset_id = excluded.media_asset_id,
			original_name = excluded.original_name,
			file_size = excluded.file_size,
			sha256 = excluded.sha256,
			sync_status = 'verified',
			verified_at = excluded.verified_at,
			updated_at = excluded.updated_at
	`, opts.DeviceID, opts.DeviceAssetID, assetID, opts.OriginalName,
		size, hash, now, now, now)
	return err
}

func (s *Service) ListAssets(page, limit int, kind, deviceID, month string) ([]Asset, int, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	offset := (page - 1) * limit
	var total int
	where := ` WHERE (? = '' OR media_kind = ?)
		AND (? = '' OR strftime('%Y-%m', COALESCE(captured_at, imported_at)) = ?)
		AND (? = '' OR EXISTS (SELECT 1 FROM device_assets da
			WHERE da.media_asset_id = media_assets.id AND da.device_id = ?))`
	args := []interface{}{kind, kind, month, month, deviceID, deviceID}
	err := s.db.QueryRow(`SELECT COUNT(*) FROM media_assets`+where, args...).Scan(&total)
	var rows *sql.Rows
	if err == nil {
		rows, err = s.db.Query(assetSelect+where+`
			ORDER BY COALESCE(captured_at, imported_at) DESC, id DESC LIMIT ? OFFSET ?`,
			append(args, limit, offset)...)
	}
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	assets := make([]Asset, 0, limit)
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err == nil {
			s.decorateAsset(&asset)
			assets = append(assets, asset)
		}
	}
	return assets, total, rows.Err()
}

func (s *Service) GetAsset(id string) (*Asset, error) {
	row := s.db.QueryRow(assetSelect+` WHERE id = ? OR hash = ? LIMIT 1`, id, id)
	asset, err := scanAsset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.decorateAsset(&asset)
	return &asset, nil
}

const assetSelect = `
	SELECT id, hash, original_name, media_kind, mime_type, file_size, status,
		captured_at, imported_at, width, height, duration_ms,
		checksum_verified, preview_status,
		COALESCE(source_device_id, ''), COALESCE(device_asset_id, '')
	FROM media_assets`

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanAsset(row scanner) (Asset, error) {
	var a Asset
	var captured sql.NullTime
	var width, height sql.NullInt64
	var duration sql.NullInt64
	var verified int
	err := row.Scan(&a.ID, &a.Hash, &a.OriginalName, &a.MediaKind, &a.MIMEType,
		&a.FileSize, &a.Status, &captured, &a.ImportedAt, &width, &height,
		&duration, &verified, &a.PreviewStatus, &a.SourceDeviceID, &a.DeviceAssetID)
	if err != nil {
		return a, err
	}
	if captured.Valid {
		t := captured.Time
		a.CapturedAt = &t
	}
	if width.Valid {
		v := int(width.Int64)
		a.Width = &v
	}
	if height.Valid {
		v := int(height.Int64)
		a.Height = &v
	}
	if duration.Valid {
		v := duration.Int64
		a.DurationMS = &v
	}
	a.ChecksumVerified = verified == 1
	return a, nil
}

func (s *Service) decorateAsset(a *Asset) {
	a.ContentURL = "/api/v1/media/" + a.ID + "/content"
	if a.PreviewStatus == "ready" {
		a.ThumbnailURL = "/api/v1/media/" + a.ID + "/thumbnail?size=small"
	}
	var primaryPath, backupPath string
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(CASE WHEN storage_name = 'primary' THEN storage_path END), ''),
		COALESCE(MAX(CASE WHEN storage_name = 'backup' THEN storage_path END), '')
		FROM storage_copies WHERE media_asset_id = ? AND status = 'verified'
		AND checksum_verified = 1`, a.ID).Scan(&primaryPath, &backupPath)
	if primaryPath != "" {
		a.StorageCopies++
	}
	if backupPath != "" {
		a.StorageCopies++
	}
	// A single laptop copy is an offload. It becomes safe-to-delete only after
	// another independently verified storage copy exists.
	if a.StorageCopies == 2 && s.backupAvailable() == nil {
		_, primaryErr := os.Stat(primaryPath)
		_, backupErr := os.Stat(backupPath)
		a.SafeToDelete = primaryErr == nil && backupErr == nil
	}
}

func (s *Service) ContentPath(id string) (string, string, error) {
	var path, mimeType string
	err := s.db.QueryRow(`SELECT storage_path, mime_type FROM media_assets WHERE id = ? OR hash = ? LIMIT 1`, id, id).Scan(&path, &mimeType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	return path, mimeType, nil
}

func (s *Service) ThumbnailPath(id, size string) (string, error) {
	if size != "medium" {
		size = "small"
	}
	var path string
	err := s.db.QueryRow(`SELECT storage_path FROM derivatives WHERE media_asset_id = ? AND kind = ?`, id, "thumbnail_"+size).Scan(&path)
	if err == nil {
		return path, nil
	}
	if errors.Is(err, sql.ErrNoRows) && len(id) >= 2 {
		legacy := filepath.Join(s.cacheDir, "thumbnails", size, id[:2], id+".jpg")
		if _, statErr := os.Stat(legacy); statErr == nil {
			return legacy, nil
		}
		return "", ErrNotFound
	}
	return "", err
}

func (s *Service) CreateUpload(ownerID string, req CreateUploadRequest) (*UploadSession, error) {
	if err := s.primaryAvailable(); err != nil {
		return nil, err
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, ErrForbidden
	}
	if strings.TrimSpace(req.OriginalName) == "" {
		return nil, fmt.Errorf("original_name is required")
	}
	if req.ExpectedSize < 0 || req.ExpectedSize > s.maxSize {
		return nil, fmt.Errorf("invalid or oversized asset")
	}
	if err := s.ensureCapacity(req.ExpectedSize); err != nil {
		return nil, err
	}
	if req.ExpectedSHA256 != "" && !isSHA256(req.ExpectedSHA256) {
		return nil, fmt.Errorf("sha256 must be a 64-character hexadecimal value")
	}
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	tempPath := filepath.Join(s.storageRoot, ".partial", id+".part")
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	expires := now.Add(48 * time.Hour)
	mimeType := strings.TrimSpace(req.MIMEType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	_, err = s.db.Exec(`
		INSERT INTO upload_sessions (
			id, owner_id, device_id, device_asset_id, original_name, mime_type,
			expected_size, expected_sha256, received_size, temp_path, status,
			created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'uploading', ?, ?, ?)
	`, id, ownerID, nullIfEmpty(req.DeviceID), nullIfEmpty(req.DeviceAssetID),
		req.OriginalName, mimeType, req.ExpectedSize, strings.ToLower(req.ExpectedSHA256),
		tempPath, now, now, expires)
	if err != nil {
		_ = os.Remove(tempPath)
		return nil, err
	}
	return s.GetUpload(id, ownerID)
}

func (s *Service) GetUpload(id, ownerID string) (*UploadSession, error) {
	var u UploadSession
	var deviceID, deviceAssetID, expectedHash, assetID, errorMessage sql.NullString
	err := s.db.QueryRow(`
		SELECT id, owner_id, device_id, device_asset_id, original_name, mime_type,
			expected_size, expected_sha256, received_size, temp_path, status,
			media_asset_id, error_message, created_at, updated_at, expires_at
		FROM upload_sessions WHERE id = ?
	`, id).Scan(&u.ID, &u.OwnerID, &deviceID, &deviceAssetID, &u.OriginalName,
		&u.MIMEType, &u.ExpectedSize, &expectedHash, &u.ReceivedSize, &u.TempPath,
		&u.Status, &assetID, &errorMessage, &u.CreatedAt, &u.UpdatedAt, &u.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if u.OwnerID != ownerID {
		return nil, ErrForbidden
	}
	u.DeviceID = deviceID.String
	u.DeviceAssetID = deviceAssetID.String
	u.ExpectedSHA256 = expectedHash.String
	u.MediaAssetID = assetID.String
	u.ErrorMessage = errorMessage.String
	return &u, nil
}

func (s *Service) AppendChunk(id, ownerID string, offset int64, data []byte) (*UploadSession, error) {
	if int64(len(data)) > MaxChunkSize {
		return nil, fmt.Errorf("chunk exceeds %d bytes", MaxChunkSize)
	}
	mu := s.uploadMutex(id)
	mu.Lock()
	defer mu.Unlock()
	u, err := s.GetUpload(id, ownerID)
	if err != nil {
		return nil, err
	}
	if u.Status != "uploading" {
		return nil, fmt.Errorf("upload is %s", u.Status)
	}
	if time.Now().After(u.ExpiresAt) {
		_, _ = s.db.Exec(`UPDATE upload_sessions SET status = 'expired', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
		return nil, ErrUploadExpired
	}
	if offset != u.ReceivedSize {
		return u, ErrOffset
	}
	if offset+int64(len(data)) > u.ExpectedSize {
		return nil, fmt.Errorf("chunk exceeds expected upload size")
	}
	f, err := os.OpenFile(u.TempPath, os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.WriteAt(data, offset)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	newOffset := offset + int64(len(data))
	_, err = s.db.Exec(`UPDATE upload_sessions SET received_size = ?, updated_at = ? WHERE id = ?`, newOffset, time.Now().UTC(), id)
	if err != nil {
		return nil, err
	}
	return s.GetUpload(id, ownerID)
}

func (s *Service) CompleteUpload(id, ownerID string) (*ImportResult, error) {
	mu := s.uploadMutex(id)
	mu.Lock()
	defer mu.Unlock()
	u, err := s.GetUpload(id, ownerID)
	if err != nil {
		return nil, err
	}
	if u.Status == "completed" && u.MediaAssetID != "" {
		asset, err := s.GetAsset(u.MediaAssetID)
		return &ImportResult{Asset: asset, Duplicate: true}, err
	}
	if u.Status != "uploading" {
		return nil, fmt.Errorf("upload is %s", u.Status)
	}
	if u.ReceivedSize != u.ExpectedSize {
		return nil, fmt.Errorf("upload incomplete: received %d of %d bytes", u.ReceivedSize, u.ExpectedSize)
	}
	actualHash, err := hashFile(u.TempPath)
	if err != nil {
		return nil, err
	}
	if u.ExpectedSHA256 != "" && !strings.EqualFold(actualHash, u.ExpectedSHA256) {
		_, _ = s.db.Exec(`UPDATE upload_sessions SET status = 'failed', error_message = 'sha256 mismatch', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
		return nil, fmt.Errorf("sha256 mismatch")
	}
	result, err := s.ImportFile(u.TempPath, ImportOptions{
		OriginalName:  u.OriginalName,
		MIMEType:      u.MIMEType,
		DeviceID:      u.DeviceID,
		DeviceAssetID: u.DeviceAssetID,
		MoveSource:    true,
	})
	if err != nil {
		_, _ = s.db.Exec(`UPDATE upload_sessions SET error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, err.Error(), id)
		return nil, err
	}
	_, err = s.db.Exec(`
		UPDATE upload_sessions SET status = 'completed', media_asset_id = ?,
			received_size = expected_size, error_message = NULL, updated_at = ? WHERE id = ?
	`, result.Asset.ID, time.Now().UTC(), id)
	if err == nil {
		_ = os.Remove(u.TempPath)
	}
	return result, err
}

func (s *Service) ensureCapacity(expectedSize int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.storageRoot, &st); err != nil {
		return err
	}
	free := uint64(st.Bavail) * uint64(st.Bsize)
	total := uint64(st.Blocks) * uint64(st.Bsize)
	reserve := total * 15 / 100
	if reserve < 1<<30 {
		reserve = 1 << 30
	}
	if free < reserve || uint64(expectedSize) > free-reserve {
		return fmt.Errorf("primary storage does not have enough free space")
	}
	return nil
}

func (s *Service) CancelUpload(id, ownerID string) error {
	mu := s.uploadMutex(id)
	mu.Lock()
	defer mu.Unlock()
	u, err := s.GetUpload(id, ownerID)
	if err != nil {
		return err
	}
	if u.Status == "completed" {
		return fmt.Errorf("completed upload cannot be cancelled")
	}
	_ = os.Remove(u.TempPath)
	_, err = s.db.Exec(`UPDATE upload_sessions SET status = 'cancelled', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

func (s *Service) uploadMutex(id string) *sync.Mutex {
	value, _ := s.uploadLocks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Service) assetStoragePath(hash, originalName string) string {
	ext := safeExtension(originalName)
	return filepath.Join(s.storageRoot, "originals", hash[:2], hash+ext)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func detectMIMEType(path, name, provided string) string {
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		if n > 0 {
			detected := http.DetectContentType(buf[:n])
			if detected != "application/octet-stream" {
				return strings.Split(detected, ";")[0]
			}
		}
	}
	if guessed := mimeFromExtension(name); guessed != "" {
		return guessed
	}
	provided = strings.TrimSpace(strings.Split(provided, ";")[0])
	if provided != "" {
		return provided
	}
	return "application/octet-stream"
}

func mimeFromExtension(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".heic", ".heif":
		return "image/heic"
	case ".gif":
		return "image/gif"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".pdf":
		return "application/pdf"
	default:
		return ""
	}
}

func mediaKind(mimeType, name string) string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	case mimeType == "application/pdf":
		return "document"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md":
		return "document"
	default:
		return "other"
	}
}

func safeExtension(name string) string {
	ext := strings.ToLower(filepath.Ext(filepath.Base(name)))
	if len(ext) < 2 || len(ext) > 12 {
		return ""
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}

func materializeOriginal(source, destination string, move bool) (bool, error) {
	if _, err := os.Stat(destination); err == nil {
		return false, nil
	}
	temp := destination + ".tmp"
	_ = os.Remove(temp)
	if move {
		if err := os.Rename(source, temp); err == nil {
			if err := syncFile(temp); err != nil {
				_ = os.Rename(temp, source)
				return false, err
			}
			if err := os.Rename(temp, destination); err != nil {
				_ = os.Rename(temp, source)
				return false, err
			}
			if err := syncDirectory(filepath.Dir(destination)); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	in, err := os.Open(source)
	if err != nil {
		return false, err
	}
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		in.Close()
		return false, err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	in.Close()
	if copyErr != nil {
		_ = os.Remove(temp)
		return false, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(temp)
		return false, closeErr
	}
	if err := os.Rename(temp, destination); err != nil {
		_ = os.Remove(temp)
		return false, err
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return false, err
	}
	if move {
		_ = os.Remove(source)
	}
	return true, nil
}

func randomHex(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sameFilePath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return aa == bb
}

func nullIfEmpty(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
