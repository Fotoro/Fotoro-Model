package cold

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Warm single-asset archive layout (all little-endian):
//
//	[8B magic "FTRCOLD1"][1B codec][8B raw_size][payload]
//
// The header keeps each archive self-describing; raw_size is a cheap sanity
// check for the decoded length before the caller's SHA-256 verification.
var fileMagic = []byte("FTRCOLD1")

const fileHeaderLen = 8 + 1 + 8

// WriteFile atomically writes a warm archive file: temp file, fsync, rename.
func WriteFile(path string, codec Codec, rawSize int64, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ftrcold-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	hdr := make([]byte, fileHeaderLen)
	copy(hdr, fileMagic)
	hdr[8] = byte(codec)
	binary.LittleEndian.PutUint64(hdr[9:], uint64(rawSize))
	if _, err := tmp.Write(hdr); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadFileHeader returns codec and original size without reading the payload.
func ReadFileHeader(path string) (Codec, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	hdr := make([]byte, fileHeaderLen)
	if _, err := f.ReadAt(hdr, 0); err != nil {
		return 0, 0, err
	}
	if string(hdr[:8]) != string(fileMagic) {
		return 0, 0, errors.New("bad archive magic")
	}
	return Codec(hdr[8]), int64(binary.LittleEndian.Uint64(hdr[9:])), nil
}

// ReadFile returns the decoded original bytes of a warm archive.
func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() < fileHeaderLen {
		return nil, errors.New("archive too small")
	}
	data := make([]byte, st.Size())
	if _, err := f.ReadAt(data, 0); err != nil {
		return nil, err
	}
	if string(data[:8]) != string(fileMagic) {
		return nil, errors.New("bad archive magic")
	}
	rawSize := int64(binary.LittleEndian.Uint64(data[9:]))
	out, err := Decode(Codec(data[8]), data[fileHeaderLen:])
	if err != nil {
		return nil, fmt.Errorf("decode archive member: %w", err)
	}
	if int64(len(out)) != rawSize {
		return nil, fmt.Errorf("decoded size %d != header %d", len(out), rawSize)
	}
	return out, nil
}
