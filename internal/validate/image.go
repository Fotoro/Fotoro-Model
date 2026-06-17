package validate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/corona10/goimagehash"
	"github.com/disintegration/imaging"
	"github.com/rwcarlsen/goexif/exif"
)

const (
	VLMSize     = 336
	ThumbSmall  = 256
	ThumbMedium = 1024
	MaxFileSize = 50 * 1024 * 1024
)

type ImageMeta struct {
	Path        string
	Hash        string
	VLMBytes    []byte
	ThumbSmall  []byte
	ThumbMedium []byte
	Width       int
	Height      int
	TakenAt     time.Time
	PHash       string
}

func FastHash(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > MaxFileSize {
		return "", fmt.Errorf("file too large: %d MB", info.Size()/1024/1024)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func extractDateTaken(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	x, err := exif.Decode(f)
	if err != nil {
		return time.Time{}
	}
	t, err := x.DateTime()
	if err != nil {
		return time.Time{}
	}
	return t
}

func computePHash(img image.Image) string {
	hash, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return ""
	}
	return hash.ToString()
}

func PrepareImage(path string) (*ImageMeta, error) {
	hash, err := FastHash(path)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}
	cfg, format, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	w, h := cfg.Width, cfg.Height
	if w > 20000 || h > 20000 {
		return nil, fmt.Errorf("suspicious dimensions: %dx%d", w, h)
	}

	var img image.Image
	if format == "jpeg" || format == "jpg" {
		img, err = imaging.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open image: %w", err)
		}
	} else {
		img, err = imaging.Open(path, imaging.AutoOrientation(true))
		if err != nil {
			return nil, fmt.Errorf("open image: %w", err)
		}
	}

	w = img.Bounds().Dx()
	h = img.Bounds().Dy()

	takenAt := extractDateTaken(path)
	phash := computePHash(img)

	vlmImg := img
	if w > VLMSize || h > VLMSize {
		vlmImg = imaging.Fit(img, VLMSize, VLMSize, imaging.Box)
	}
	var vlmBuf bytes.Buffer
	if err := imaging.Encode(&vlmBuf, vlmImg, imaging.JPEG, imaging.JPEGQuality(80)); err != nil {
		return nil, err
	}

	smallImg := imaging.Fit(img, ThumbSmall, ThumbSmall, imaging.Box)
	var smallBuf bytes.Buffer
	if err := imaging.Encode(&smallBuf, smallImg, imaging.JPEG, imaging.JPEGQuality(75)); err != nil {
		return nil, err
	}

	medImg := imaging.Fit(img, ThumbMedium, ThumbMedium, imaging.Lanczos)
	var medBuf bytes.Buffer
	if err := imaging.Encode(&medBuf, medImg, imaging.JPEG, imaging.JPEGQuality(80)); err != nil {
		return nil, err
	}

	return &ImageMeta{
		Path:        path,
		Hash:        hash,
		VLMBytes:    vlmBuf.Bytes(),
		ThumbSmall:  smallBuf.Bytes(),
		ThumbMedium: medBuf.Bytes(),
		Width:       w,
		Height:      h,
		TakenAt:     takenAt,
		PHash:       phash,
	}, nil
}

func SaveThumbnails(baseDir string, meta *ImageMeta) error {
	for _, size := range []struct {
		name string
		data []byte
	}{
		{"small", meta.ThumbSmall},
		{"medium", meta.ThumbMedium},
	} {
		dir := filepath.Join(baseDir, "thumbnails", size.name, meta.Hash[:2])
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, meta.Hash+".jpg"), size.data, 0644); err != nil {
			return err
		}
	}
	return nil
}
