package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fotoro/internal/db"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(filepath.Join(root, "fotoro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s, err := NewService(database, filepath.Join(root, "fotoro.db"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResumableUploadAndBackup(t *testing.T) {
	s := newTestService(t)
	data := []byte("original media bytes for a resumable upload")
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	u, err := s.CreateUpload("owner-1", CreateUploadRequest{
		OriginalName: "camera.jpg", MIMEType: "image/jpeg", ExpectedSize: int64(len(data)),
		ExpectedSHA256: hash, DeviceID: "phone-1", DeviceAssetID: "asset-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendChunk(u.ID, "owner-1", 0, data[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendChunk(u.ID, "owner-1", 0, data[8:]); err != ErrOffset {
		t.Fatalf("wrong offset: got %v", err)
	}
	if _, err := s.AppendChunk(u.ID, "owner-2", 8, data[8:]); err != ErrForbidden {
		t.Fatalf("wrong owner: got %v", err)
	}
	if _, err := s.AppendChunk(u.ID, "owner-1", 8, data[8:]); err != nil {
		t.Fatal(err)
	}
	result, err := s.CompleteUpload(u.ID, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Asset.Hash != hash || result.Asset.SafeToDelete || result.Asset.StorageCopies != 1 {
		t.Fatalf("unexpected primary state: %+v", result.Asset)
	}
	path, _, err := s.ContentPath(hash)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("original changed: %v", err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err := os.Mkdir(backup, 0700); err != nil {
		t.Fatal(err)
	}
	s.backupRoot = backup
	s.backupDevice = s.primaryDevice + 1
	s.deviceForPath = func(path string) (uint64, error) {
		if strings.HasPrefix(path, backup) {
			return s.backupDevice, nil
		}
		return s.primaryDevice, nil
	}
	count, err := s.BackupPending(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("backup pass: count=%d err=%v", count, err)
	}
	asset, err := s.GetAsset(hash)
	if err != nil || !asset.SafeToDelete || asset.StorageCopies != 2 {
		t.Fatalf("expected two verified copies: asset=%+v err=%v", asset, err)
	}
	if err := os.Remove(filepath.Join(backup, "originals", hash[:2], hash+".jpg")); err != nil {
		t.Fatal(err)
	}
	asset, err = s.GetAsset(hash)
	if err != nil || asset.SafeToDelete {
		t.Fatalf("missing secondary must not be safe: asset=%+v err=%v", asset, err)
	}
}

func TestHashMismatchNeverCommitsOriginal(t *testing.T) {
	s := newTestService(t)
	u, err := s.CreateUpload("owner", CreateUploadRequest{
		OriginalName: "bad.jpg", ExpectedSize: 3, ExpectedSHA256: strings.Repeat("0", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendChunk(u.ID, "owner", 0, []byte("bad")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteUpload(u.ID, "owner"); err == nil {
		t.Fatal("expected hash mismatch")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM media_assets`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("mismatched upload committed: count=%d err=%v", count, err)
	}
}
