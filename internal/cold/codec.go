// Package cold implements Fotoro's lossless cold-storage codecs and archive
// containers. Every byte produced here decodes back to the original bytes,
// which lets the tier engine verify each restore against the asset's SHA-256
// id before removing a raw original.
package cold

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"

	"fotoro/internal/jxl"
)

// Codec identifies the member encoding inside an archive file or pack.
// It is recorded per asset in media_assets.archive_codec and per pack member
// in the pack index; decode is always dispatched on the recorded value.
type Codec uint8

const (
	CodecNone Codec = iota // member holds raw original bytes
	CodecZstd              // klauspost zstd frame
	CodecJXL               // JPEG XL lossless JPEG transcode
)

func (c Codec) String() string {
	switch c {
	case CodecZstd:
		return "zstd"
	case CodecJXL:
		return "jxl"
	default:
		return "none"
	}
}

func ParseCodec(s string) (Codec, error) {
	switch s {
	case "none", "":
		return CodecNone, nil
	case "zstd":
		return CodecZstd, nil
	case "jxl":
		return CodecJXL, nil
	}
	return 0, fmt.Errorf("unknown codec %q", s)
}

// Choose picks a codec from the file extension. JPEG originals get JPEG XL
// (only codec that shrinks an existing JPEG codestream while preserving
// bit-exact restore). Compressible raws get zstd; already-compressed formats
// stay raw so restore stays a memcpy.
func Choose(name string) Codec {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg", ".jpe":
		if jxl.Available() {
			return CodecJXL
		}
		return CodecZstd
	case ".tif", ".tiff", ".bmp", ".dng", ".cr2", ".cr3", ".nef", ".nrw",
		".arw", ".orf", ".rw2", ".pef", ".srw", ".raf", ".raw", ".psd",
		".xcf", ".ppm", ".pgm", ".pbm", ".tga", ".exr", ".hdr", ".svg",
		".txt", ".csv", ".log", ".json", ".xml", ".wav", ".aif", ".aiff":
		return CodecZstd
	default:
		return CodecNone
	}
}

// Encode compresses src with codec. When compression would not save at least
// 1% the result is the raw bytes with CodecNone so the format stays uniform.
// The returned codec is what must be recorded alongside the blob.
func Encode(codec Codec, src []byte) ([]byte, Codec, error) {
	switch codec {
	case CodecNone:
		return src, CodecNone, nil
	case CodecJXL:
		blob, err := jxl.EncodeJPEG(src, 4)
		if err != nil {
			return nil, 0, err
		}
		return blob, CodecJXL, nil
	case CodecZstd:
		blob, err := zstdEncode(src)
		if err != nil {
			return nil, 0, err
		}
		if len(blob) >= len(src)-len(src)/100 {
			return src, CodecNone, nil
		}
		return blob, CodecZstd, nil
	}
	return nil, 0, fmt.Errorf("unknown codec %d", codec)
}

// Decode reverses Encode. Callers verify the output hashes to the asset id.
func Decode(codec Codec, blob []byte) ([]byte, error) {
	switch codec {
	case CodecNone:
		return blob, nil
	case CodecJXL:
		return jxl.DecodeJPEG(blob)
	case CodecZstd:
		return zstdDecode(blob)
	}
	return nil, fmt.Errorf("unknown codec %d", codec)
}

var (
	zstdEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	zstdDec, _ = zstd.NewReader(nil)
)

func zstdEncode(src []byte) ([]byte, error) {
	return zstdEnc.EncodeAll(src, nil), nil
}

func zstdDecode(blob []byte) ([]byte, error) {
	return zstdDec.DecodeAll(blob, nil)
}
