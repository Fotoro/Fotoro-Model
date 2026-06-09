package validate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/disintegration/imaging"
)

const (
	VLMSize     = 448
	ThumbSmall  = 256
	ThumbMedium = 1024
	MaxFileSize = 50 * 1024 * 1024 // 50MB
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

	// FIX: AutoOrientation is a DecodeOption, passed to Open
	img, err := imaging.Open(path, imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}

	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w > 20000 || h > 20000 {
		return nil, fmt.Errorf("suspicious dimensions: %dx%d", w, h)
	}

	// VLM input
	vlmImg := img
	if w > VLMSize || h > VLMSize {
		vlmImg = imaging.Fit(img, VLMSize, VLMSize, imaging.Lanczos)
	}
	var vlmBuf bytes.Buffer
	if err := imaging.Encode(&vlmBuf, vlmImg, imaging.JPEG, imaging.JPEGQuality(90)); err != nil {
		return nil, err
	}

	// Small thumb
	smallImg := imaging.Fit(img, ThumbSmall, ThumbSmall, imaging.Box)
	var smallBuf bytes.Buffer
	if err := imaging.Encode(&smallBuf, smallImg, imaging.JPEG, imaging.JPEGQuality(80)); err != nil {
		return nil, err
	}

	// Medium thumb
	medImg := imaging.Fit(img, ThumbMedium, ThumbMedium, imaging.Lanczos)
	var medBuf bytes.Buffer
	if err := imaging.Encode(&medBuf, medImg, imaging.JPEG, imaging.JPEGQuality(85)); err != nil {
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