package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
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

func TestBackupMountMustRemainIndependent(t *testing.T) {
	s := newTestService(t)
	backup := filepath.Join(t.TempDir(), "mounted", "library")
	if err := os.MkdirAll(backup, 0700); err != nil {
		t.Fatal(err)
	}
	s.backupRoot = backup
	s.backupMount = filepath.Dir(backup)
	s.backupDevice = s.primaryDevice + 1
	s.deviceForPath = func(path string) (uint64, error) {
		if strings.HasPrefix(path, s.backupMount) {
			return s.backupDevice, nil
		}
		return s.primaryDevice, nil
	}
	if err := s.backupAvailable(); err != nil {
		t.Fatalf("mounted secondary should be available: %v", err)
	}
	s.deviceForPath = func(string) (uint64, error) { return s.primaryDevice, nil }
	if err := s.backupAvailable(); err == nil {
		t.Fatal("an unmounted secondary was accepted")
	}
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
	page, total, err := s.ListAssets(1, 100, "", "", "")
	if err != nil || total != 1 || len(page) != 1 || page[0].StorageCopies != 1 {
		t.Fatalf("gallery page after offload: total=%d page=%+v err=%v", total, page, err)
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
	s.backupMount = backup
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
	page, total, err = s.ListAssets(1, 100, "", "", "")
	if err != nil || total != 1 || len(page) != 1 || !page[0].SafeToDelete {
		t.Fatalf("gallery page lost verified backup state: total=%d page=%+v err=%v", total, page, err)
	}
	backupPath := filepath.Join(backup, "originals", hash[:2], hash+".jpg")
	if err := os.WriteFile(backupPath, []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	checked, err := s.VerifyBackupCopies(context.Background(), 10)
	if err != nil || checked != 1 {
		t.Fatalf("integrity scan: checked=%d err=%v", checked, err)
	}
	asset, err = s.GetAsset(hash)
	if err != nil || asset.SafeToDelete {
		t.Fatalf("corrupt secondary must not be safe: asset=%+v err=%v", asset, err)
	}
	count, err = s.BackupPending(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("repair pass: count=%d err=%v", count, err)
	}
	asset, err = s.GetAsset(hash)
	if err != nil || !asset.SafeToDelete {
		t.Fatalf("repaired secondary should be safe: asset=%+v err=%v", asset, err)
	}
}

func BenchmarkListAssets100Of3000(b *testing.B) {
	root := b.TempDir()
	database, err := db.Open(filepath.Join(root, "fotoro.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	s, err := NewService(database, filepath.Join(root, "fotoro.db"))
	if err != nil {
		b.Fatal(err)
	}
	tx, err := database.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("%064x", i+1)
		if _, err := tx.Exec(`INSERT INTO media_assets (id, hash, original_name, storage_path, media_kind)
			VALUES (?, ?, 'photo.jpg', '/fixture/photo.jpg', 'image')`, id, id); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page, total, err := s.ListAssets(i%30+1, 100, "", "", "")
		if err != nil || total != 3000 || len(page) != 100 {
			b.Fatalf("page=%d total=%d err=%v", len(page), total, err)
		}
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

func TestLegacyRowRequiresRealPrimaryVerification(t *testing.T) {
	s := newTestService(t)
	contents := []byte("legacy original")
	digest := sha256.Sum256(contents)
	hash := hex.EncodeToString(digest[:])
	path := filepath.Join(t.TempDir(), "legacy.jpg")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO media_assets
		(id, hash, original_name, storage_path, media_kind, file_size, checksum_verified)
		VALUES (?, ?, 'legacy.jpg', ?, 'image', 0, 1)`, hash, hash, path); err != nil {
		t.Fatal(err)
	}
	asset, err := s.GetAsset(hash)
	if err != nil || asset.StorageCopies != 0 || asset.SafeToDelete {
		t.Fatalf("backfill flag is not copy proof: asset=%+v err=%v", asset, err)
	}
	verified, err := s.VerifyPrimaryPending(context.Background(), 10)
	if err != nil || verified != 1 {
		t.Fatalf("primary verification: count=%d err=%v", verified, err)
	}
	asset, err = s.GetAsset(hash)
	if err != nil || asset.FileSize != int64(len(contents)) || asset.StorageCopies != 1 {
		t.Fatalf("legacy reconciliation: asset=%+v err=%v", asset, err)
	}
}

func TestThumbnailJobRunsAfterDurableImport(t *testing.T) {
	s := newTestService(t)
	path := filepath.Join(t.TempDir(), "camera.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), A: 255})
		}
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	result, err := s.ImportFile(path, ImportOptions{OriginalName: "camera.png"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Asset.PreviewStatus != "pending" || result.Asset.ThumbnailURL != "" {
		t.Fatalf("upload acknowledgement waited for preview: %+v", result.Asset)
	}
	processed, err := s.ProcessDerivativeJobs(context.Background(), 1)
	if err != nil || processed != 1 {
		t.Fatalf("thumbnail job: processed=%d err=%v", processed, err)
	}
	asset, err := s.GetAsset(result.Asset.ID)
	if err != nil || asset.PreviewStatus != "ready" || asset.ThumbnailURL == "" {
		t.Fatalf("thumbnail unavailable: asset=%+v err=%v", asset, err)
	}
	thumb, err := s.ThumbnailPath(asset.ID, "small")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(thumb); err != nil {
		t.Fatal(err)
	}
}
