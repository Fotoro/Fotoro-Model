package jxl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// testJPEG returns a baseline JPEG. When FOTORO_TEST_JPEG points at a real
// library original it is used; otherwise a gradient is encoded in-process.
func testJPEG(t *testing.T) []byte {
	t.Helper()
	if p := os.Getenv("FOTORO_TEST_JPEG"); p != "" {
		data, err := os.ReadFile(p)
		if err == nil && len(data) > 4 {
			return data
		}
	}
	const w, h = 640, 480
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.YOffset(x, y)
			img.Y[i] = byte((x*3 + y*5) % 256)
		}
	}
	for i := range img.Cb {
		img.Cb[i] = byte(i % 256)
	}
	for i := range img.Cr {
		img.Cr[i] = byte((2*i + 17) % 256)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return buf.Bytes()
}

func TestRoundTripBitExact(t *testing.T) {
	src := testJPEG(t)
	jxlData, err := EncodeJPEG(src, 4)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(jxlData) == 0 {
		t.Fatal("empty JXL output")
	}
	t.Logf("jpeg %d -> jxl %d (%.1f%%)", len(src), len(jxlData),
		100*float64(len(jxlData))/float64(len(src)))
	back, err := DecodeJPEG(jxlData)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	srcSum := sha256.Sum256(src)
	backSum := sha256.Sum256(back)
	if srcSum != backSum {
		t.Fatalf("round-trip mismatch: %s vs %s",
			hex.EncodeToString(srcSum[:8]),
			hex.EncodeToString(backSum[:8]))
	}
	if !bytes.Equal(src, back) {
		t.Fatal("round-trip bytes differ")
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := DecodeJPEG([]byte{0x01, 0x02, 0x03, 0x04}); err == nil {
		t.Fatal("expected decode error on garbage")
	}
}

func TestEncodeRejectsNonJPEG(t *testing.T) {
	if _, err := EncodeJPEG([]byte("not a jpeg"), 4); err == nil {
		t.Fatal("expected encode error on non-JPEG")
	}
}

// TestLibraryImageIfPresent round-trips real phone photos when the library is
// on disk:
//
//	go test ./internal/jxl -run TestLibraryImageIfPresent -v
func TestLibraryImageIfPresent(t *testing.T) {
	root := filepath.Join("..", "..", "library", "originals")
	entries, _ := filepath.Glob(filepath.Join(root, "*", "*.jpg"))
	if len(entries) == 0 {
		t.Skip("no library originals present")
	}
	if len(entries) > 5 {
		entries = entries[:5]
	}
	var totalIn, totalOut int64
	for _, p := range entries {
		src, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		jxlData, err := EncodeJPEG(src, 4)
		if err != nil {
			t.Fatalf("encode %s: %v", filepath.Base(p), err)
		}
		back, err := DecodeJPEG(jxlData)
		if err != nil {
			t.Fatalf("decode %s: %v", filepath.Base(p), err)
		}
		if sha256.Sum256(src) != sha256.Sum256(back) {
			t.Fatalf("round-trip mismatch for %s", filepath.Base(p))
		}
		totalIn += int64(len(src))
		totalOut += int64(len(jxlData))
		t.Logf("%s: %d -> %d (%.1f%%)", filepath.Base(p)[:16],
			len(src), len(jxlData), 100*float64(len(jxlData))/float64(len(src)))
	}
	if totalIn > 0 {
		t.Logf("total: %d -> %d (%.1f%%)", totalIn, totalOut,
			100*float64(totalOut)/float64(totalIn))
	}
}
