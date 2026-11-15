package cold

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestCodecRoundTrip(t *testing.T) {
	compressible := bytes.Repeat([]byte("fotoro library test payload "), 4000)
	incompressible := make([]byte, 100000)
	rand.Read(incompressible)

	blob, c, err := Encode(CodecZstd, compressible)
	if err != nil {
		t.Fatalf("zstd encode: %v", err)
	}
	if c != CodecZstd {
		t.Fatalf("compressible data chose %v", c)
	}
	out, err := Decode(c, blob)
	if err != nil || !bytes.Equal(out, compressible) {
		t.Fatal("zstd round-trip mismatch")
	}

	blob, c, err = Encode(CodecZstd, incompressible)
	if err != nil {
		t.Fatalf("zstd encode incompressible: %v", err)
	}
	if c != CodecNone {
		t.Fatalf("incompressible data kept %v, want none", c)
	}
	out, err = Decode(c, blob)
	if err != nil || !bytes.Equal(out, incompressible) {
		t.Fatal("none round-trip mismatch")
	}

	if got := Choose("IMG_20240115_120000.jpg"); got != CodecJXL {
		t.Fatalf("jpg chose %v, want jxl", got)
	}
	if got := Choose("clip.mp4"); got != CodecNone {
		t.Fatalf("mp4 chose %v, want none", got)
	}
	if got := Choose("scan.tiff"); got != CodecZstd {
		t.Fatalf("tiff chose %v, want zstd", got)
	}
}

func TestArchiveFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	raw := bytes.Repeat([]byte("warm archive "), 2000)
	blob, c, err := Encode(CodecZstd, raw)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "ab", "deadbeef.farc")
	if err := WriteFile(p, c, int64(len(raw)), blob); err != nil {
		t.Fatalf("write: %v", err)
	}
	hc, size, err := ReadFileHeader(p)
	if err != nil || hc != c || size != int64(len(raw)) {
		t.Fatalf("header %v %d %v", hc, size, err)
	}
	back, err := ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(back, raw) {
		t.Fatal("archive round-trip mismatch")
	}
}

func TestPackRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "batches", "2024-01-01.fpack")
	w, err := NewPackWriter(p)
	if err != nil {
		t.Fatal(err)
	}
	type member struct {
		hash string
		raw  []byte
	}
	var members []member
	for i := 0; i < 3; i++ {
		raw := bytes.Repeat([]byte{byte(i), 0x42}, 5000+i*1000)
		blob, c, err := Encode(CodecZstd, raw)
		if err != nil {
			t.Fatal(err)
		}
		h := hash(raw)
		if err := w.Add(h, c, int64(len(raw)), blob); err != nil {
			t.Fatal(err)
		}
		members = append(members, member{h, raw})
	}
	if err := w.Seal(); err != nil {
		t.Fatalf("seal: %v", err)
	}

	r, err := OpenPack(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	if got := len(r.Members()); got != 3 {
		t.Fatalf("members = %d", got)
	}
	for _, m := range members {
		back, err := r.ReadMember(m.hash)
		if err != nil {
			t.Fatalf("read member: %v", err)
		}
		if !bytes.Equal(back, m.raw) {
			t.Fatalf("member %s mismatch", m.hash[:8])
		}
	}
	if _, err := r.ReadMember(hash([]byte("missing"))); err == nil {
		t.Fatal("expected missing member error")
	}
}

func TestPackRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.fpack")
	w, err := NewPackWriter(p)
	if err != nil {
		t.Fatal(err)
	}
	w.Abort()
	if err := os.WriteFile(p, []byte("not a pack"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPack(p); err == nil {
		t.Fatal("expected error on unsealed pack")
	}
}

func TestParseCodec(t *testing.T) {
	if c, err := ParseCodec("jxl"); err != nil || c != CodecJXL {
		t.Fatal("jxl parse")
	}
	if _, err := ParseCodec("bogus"); err == nil {
		t.Fatal("expected bogus codec error")
	}
}
