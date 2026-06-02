package validate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"

	"github.com/disintegration/imaging"
)

const MaxDimension = 384

type ImageResult struct {
	Path      string
	Hash      string
	JPEGBytes []byte
}

func ProcessImage(path string) (*ImageResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, fmt.Errorf("hash: %w", err)
	}
	hashStr := fmt.Sprintf("%x", h.Sum(nil))

	// Pure Go image processing
	img, err := imaging.Open(path)
	if err != nil {
		return nil, fmt.Errorf("corrupt image: %w", err)
	}

	if img.Bounds().Dx() > 20000 || img.Bounds().Dy() > 20000 {
		return nil, fmt.Errorf("suspicious dimensions: %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}

	if img.Bounds().Dx() > MaxDimension || img.Bounds().Dy() > MaxDimension {
		img = imaging.Fit(img, MaxDimension, MaxDimension, imaging.Lanczos)
	}

	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, imaging.JPEG, imaging.JPEGQuality(85)); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return &ImageResult{
		Path:      path,
		Hash:      hashStr,
		JPEGBytes: buf.Bytes(),
	}, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
