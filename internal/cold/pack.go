package cold

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Cold pack layout (all little-endian):
//
//	[member payload bytes, concatenated]
//	[8B index_len][index JSON][8B index_len][8B magic "FTRPACK1"]
//
// The index maps each member hash to its compressed extent. A reader verifies
// the tail magic, walks back index_len bytes, and reads members by seeking to
// their offset — no scan, no external tools.
var packMagic = []byte("FTRPACK1")

const packFooterLen = 8 + 8

type packIndex struct {
	V       int                   `json:"v"`
	Members map[string]PackMember `json:"members"`
}

// PackMember describes one compressed asset inside a sealed pack.
type PackMember struct {
	Offset  int64 `json:"off"`
	Length  int64 `json:"len"`
	Codec   Codec `json:"codec"`
	RawSize int64 `json:"size"`
}

// PackWriter streams members into a new pack file, then Seal writes the index.
type PackWriter struct {
	f       *os.File
	path    string
	off     int64
	members map[string]PackMember
	sealed  bool
}

func NewPackWriter(path string) (*PackWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &PackWriter{f: f, path: path, members: map[string]PackMember{}}, nil
}

// Add writes one already-compressed member payload. Hash is the asset id
// (SHA-256 hex of the original bytes); rawSize is the decoded length.
func (w *PackWriter) Add(hash string, codec Codec, rawSize int64, payload []byte) error {
	if w.sealed {
		return errors.New("pack already sealed")
	}
	if _, dup := w.members[hash]; dup {
		return fmt.Errorf("duplicate member %s", hash)
	}
	if _, err := w.f.Write(payload); err != nil {
		return err
	}
	w.members[hash] = PackMember{Offset: w.off, Length: int64(len(payload)), Codec: codec, RawSize: rawSize}
	w.off += int64(len(payload))
	return nil
}

// Offset is the byte position where the next member would be written.
func (w *PackWriter) Offset() int64 { return w.off }

// Seal appends the index footer and fsyncs; the pack is complete only after
// this returns nil.
func (w *PackWriter) Seal() error {
	if w.sealed {
		return errors.New("pack already sealed")
	}
	idx, err := json.Marshal(packIndex{V: 1, Members: w.members})
	if err != nil {
		return err
	}
	tail := make([]byte, 8+len(idx)+8+8)
	binary.LittleEndian.PutUint64(tail, uint64(len(idx)))
	copy(tail[8:], idx)
	binary.LittleEndian.PutUint64(tail[8+len(idx):], uint64(len(idx)))
	copy(tail[8+len(idx)+8:], packMagic)
	if _, err := w.f.Write(tail); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	w.sealed = true
	return w.f.Close()
}

// Abort closes and removes an unsealed pack.
func (w *PackWriter) Abort() {
	if !w.sealed {
		w.f.Close()
		os.Remove(w.path)
	}
}

// PackReader serves members from a sealed pack.
type PackReader struct {
	f    *os.File
	idx  packIndex
	path string
}

func OpenPack(path string) (*PackReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.Size() < packFooterLen+8 {
		f.Close()
		return nil, errors.New("pack too small")
	}
	foot := make([]byte, packFooterLen)
	if _, err := f.ReadAt(foot, st.Size()-packFooterLen); err != nil {
		f.Close()
		return nil, err
	}
	if string(foot[8:]) != string(packMagic) {
		f.Close()
		return nil, errors.New("bad pack magic")
	}
	idxLen := int64(binary.LittleEndian.Uint64(foot))
	idxOff := st.Size() - packFooterLen - idxLen
	if idxOff < 8 {
		f.Close()
		return nil, errors.New("bad index offset")
	}
	pre := make([]byte, 8)
	if _, err := f.ReadAt(pre, idxOff-8); err != nil {
		f.Close()
		return nil, err
	}
	if int64(binary.LittleEndian.Uint64(pre)) != idxLen {
		f.Close()
		return nil, errors.New("index length mismatch")
	}
	raw := make([]byte, idxLen)
	if _, err := f.ReadAt(raw, idxOff); err != nil {
		f.Close()
		return nil, err
	}
	var idx packIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		f.Close()
		return nil, fmt.Errorf("bad pack index: %w", err)
	}
	if idx.V != 1 {
		f.Close()
		return nil, fmt.Errorf("unsupported pack version %d", idx.V)
	}
	return &PackReader{f: f, idx: idx, path: path}, nil
}

func (r *PackReader) Close() error { return r.f.Close() }

// Members lists member hashes in deterministic order.
func (r *PackReader) Members() []string {
	out := make([]string, 0, len(r.idx.Members))
	for h := range r.idx.Members {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func (r *PackReader) Member(hash string) (PackMember, bool) {
	m, ok := r.idx.Members[hash]
	return m, ok
}

// ReadMember returns the decoded original bytes of a member.
func (r *PackReader) ReadMember(hash string) ([]byte, error) {
	blob, rawSize, codec, err := r.ReadMemberRaw(hash)
	if err != nil {
		return nil, err
	}
	out, err := Decode(codec, blob)
	if err != nil {
		return nil, fmt.Errorf("decode pack member %s: %w", hash, err)
	}
	if int64(len(out)) != rawSize {
		return nil, fmt.Errorf("decoded size %d != index %d", len(out), rawSize)
	}
	return out, nil
}

// ReadMemberRaw returns the stored payload without decoding — used by pack
// compaction which moves bytes rather than recompressing them.
func (r *PackReader) ReadMemberRaw(hash string) ([]byte, int64, Codec, error) {
	m, ok := r.idx.Members[hash]
	if !ok {
		return nil, 0, 0, fmt.Errorf("member %s not in pack", hash)
	}
	buf := make([]byte, m.Length)
	if _, err := r.f.ReadAt(buf, m.Offset); err != nil && err != io.EOF {
		return nil, 0, 0, err
	}
	return buf, m.RawSize, m.Codec, nil
}
