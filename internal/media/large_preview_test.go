package media

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestLargePreviewBackfillKeepsOriginal(t *testing.T) {
	s := newTestService(t)
	source := filepath.Join(t.TempDir(), "large.jpg")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2200, 1100))
	for y := 0; y < 1100; y++ {
		for x := 0; x < 2200; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	if err := jpeg.Encode(file, img, &jpeg.Options{Quality: 90}); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	imported, err := s.ImportFile(source, ImportOptions{OriginalName: "large.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	id := imported.Asset.ID
	processed, err := s.ProcessDerivativeJobs(context.Background(), 1)
	if err != nil || processed != 1 {
		t.Fatalf("derivative job: %d, %v", processed, err)
	}
	largePath, err := s.ThumbnailPath(id, "large")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := os.Open(largePath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := jpeg.DecodeConfig(preview)
	preview.Close()
	if err != nil || config.Width > 2048 || config.Height > 2048 {
		t.Fatalf("large preview dimensions: %+v, %v", config, err)
	}
	if err := os.Remove(largePath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM derivatives WHERE media_asset_id = ? AND kind = 'thumbnail_large'`, id); err != nil {
		t.Fatal(err)
	}
	count, err := s.BackfillLargePreviews(context.Background(), 1)
	if err != nil || count != 1 {
		t.Fatalf("backfill: %d, %v", count, err)
	}
	asset, err := s.GetAsset(id)
	if err != nil || asset.DetailURL == "" {
		t.Fatalf("display detail unavailable: %+v, %v", asset, err)
	}
	originalPath, _, err := s.ContentPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if hash, err := hashFile(originalPath); err != nil || hash != id {
		t.Fatalf("original changed: %v", err)
	}
	if count, err := s.BackfillLargePreviews(context.Background(), 1); err != nil || count != 0 {
		t.Fatalf("backfill repeated completed work: %d, %v", count, err)
	}
}
