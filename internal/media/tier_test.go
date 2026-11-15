package media

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePNG(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 4), B: 200, A: 255})
		}
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// TestTierLifecycle exercises demote → warm serve → pack → cold serve →
// promote with byte-exact verification at every step.
func TestTierLifecycle(t *testing.T) {
	s := newTestService(t)
	path := filepath.Join(t.TempDir(), "camera.tiff")
	writePNG(t, path)
	res, err := s.ImportFile(path, ImportOptions{OriginalName: "camera.tiff"})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Asset.ID

	p, mime, err := s.ContentPathTiered(id)
	if err != nil {
		t.Fatal(err)
	}
	hot, _ := os.ReadFile(p)
	if mime != "image/png" || len(hot) == 0 {
		t.Fatalf("hot serve: mime=%s bytes=%d", mime, len(hot))
	}

	if err := s.DemoteAsset(id); err != nil {
		t.Fatalf("demote: %v", err)
	}
	var tier, codec string
	var archiveSize int64
	if err := s.db.QueryRow(`SELECT tier, archive_codec, archive_size FROM media_assets WHERE id = ?`,
		id).Scan(&tier, &codec, &archiveSize); err != nil {
		t.Fatal(err)
	}
	if tier != TierWarm || codec == "" || archiveSize == 0 {
		t.Fatalf("after demote: tier=%s codec=%s size=%d", tier, codec, archiveSize)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("original still on disk after demote")
	}

	// Warm serve must rehydrate and produce identical bytes.
	s.TrackAccess(id)
	p2, _, err := s.ContentPathTiered(id)
	if err != nil {
		t.Fatalf("warm serve: %v", err)
	}
	warm, err := os.ReadFile(p2)
	if err != nil || string(warm) != string(hot) {
		t.Fatal("warm restore bytes differ")
	}

	// Pack the warm asset into a cold batch.
	packRes, err := s.PackWarmAssets(10)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if len(packRes.Packed) != 1 {
		t.Fatalf("packed = %v errors = %v", packRes.Packed, packRes.Errors)
	}
	if err := s.db.QueryRow(`SELECT tier FROM media_assets WHERE id = ?`, id).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != TierCold {
		t.Fatalf("after pack: tier=%s", tier)
	}
	var packPath string
	if err := s.db.QueryRow(`SELECT p.storage_path FROM cold_members cm
		JOIN cold_packs p ON p.id = cm.pack_id
		WHERE cm.media_asset_id = ? AND cm.state = 'live'`, id).Scan(&packPath); err != nil {
		t.Fatal(err)
	}

	p3, _, err := s.ContentPathTiered(id)
	if err != nil {
		t.Fatalf("cold serve: %v", err)
	}
	coldBytes, err := os.ReadFile(p3)
	if err != nil || string(coldBytes) != string(hot) {
		t.Fatal("cold restore bytes differ")
	}

	// Compact path: no-op while pack is fully live.
	if n, err := s.CompactPacks(1); err != nil || n != 0 {
		t.Fatalf("compact live pack: n=%d err=%v", n, err)
	}

	if err := s.PromoteAsset(id); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := s.db.QueryRow(`SELECT tier FROM media_assets WHERE id = ?`, id).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != TierHot {
		t.Fatalf("after promote: tier=%s", tier)
	}
	p4, _, err := s.ContentPathTiered(id)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(p4)
	if err != nil || string(restored) != string(hot) {
		t.Fatal("promoted original bytes differ")
	}

	// The retired member should now count toward pack dead bytes.
	var dead int64
	if err := s.db.QueryRow(`SELECT dead_bytes FROM cold_packs WHERE storage_path = ?`,
		packPath).Scan(&dead); err != nil {
		t.Fatal(err)
	}
	if dead == 0 {
		t.Fatal("promotion did not retire the cold member")
	}
	if n, err := s.CompactPacks(1); err != nil || n != 1 {
		t.Fatalf("compact dead pack: n=%d err=%v", n, err)
	}
	if err := s.db.QueryRow(`SELECT member_count FROM cold_packs WHERE storage_path = ?`,
		packPath).Scan(&dead); err == nil && dead != 0 {
		t.Fatalf("compacted pack still has %d members", dead)
	}
}

// TestDemoteRequiresVerifiedBackup refuses demotion when a second disk is
// configured but the asset has no verified copy on it.
func TestDemoteRequiresVerifiedBackup(t *testing.T) {
	s := newTestService(t)
	path := filepath.Join(t.TempDir(), "camera.tiff")
	writePNG(t, path)
	res, err := s.ImportFile(path, ImportOptions{OriginalName: "camera.tiff"})
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	s.backupRoot = backup
	s.backupMount = backup
	s.backupDevice = s.primaryDevice + 1
	s.deviceForPath = func(p string) (uint64, error) {
		if strings.HasPrefix(p, backup) {
			return s.backupDevice, nil
		}
		return s.primaryDevice, nil
	}
	if err := s.DemoteAsset(res.Asset.ID); err == nil {
		t.Fatal("demotion ran without a verified backup")
	}
	if _, err := s.BackupPending(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if err := s.DemoteAsset(res.Asset.ID); err != nil {
		t.Fatalf("demote after backup: %v", err)
	}
}
