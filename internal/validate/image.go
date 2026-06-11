package validate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"

	"github.com/disintegration/imaging"
)

const (
	// AGGRESSIVE: Drop to 336px for speed
	// 448px was accurate but 30-40s. 336px should be ~20-25s with acceptable quality.
	// Qwen2.5-VL handles 336px fine — it's still above the 224px disaster zone.
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

	// VLM input: 336px, fast resize
	vlmImg := img
	if w > VLMSize || h > VLMSize {
		vlmImg = imaging.Fit(img, VLMSize, VLMSize, imaging.Box)
	}
	var vlmBuf bytes.Buffer
	// Quality 80: smaller payload = faster network + faster vision encoder
	if err := imaging.Encode(&vlmBuf, vlmImg, imaging.JPEG, imaging.JPEGQuality(80)); err != nil {
		return nil, err
	}

	// Small thumb
	smallImg := imaging.Fit(img, ThumbSmall, ThumbSmall, imaging.Box)
	var smallBuf bytes.Buffer
	if err := imaging.Encode(&smallBuf, smallImg, imaging.JPEG, imaging.JPEGQuality(75)); err != nil {
		return nil, err
	}

	// Medium thumb
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