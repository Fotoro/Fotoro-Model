package jxl

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEffortSweep(t *testing.T) {
	entries, _ := filepath.Glob(filepath.Join("..", "..", "library", "originals", "*", "*.jpg"))
	if len(entries) == 0 {
		t.Skip("no originals")
	}
	if len(entries) > 8 {
		entries = entries[:8]
	}
	for _, effort := range []int{4, 7, 9} {
		var in, out int64
		var enc, dec time.Duration
		for _, p := range entries {
			src, _ := os.ReadFile(p)
			t0 := time.Now()
			j, err := EncodeJPEG(src, effort)
			enc += time.Since(t0)
			if err != nil {
				t.Fatalf("%s effort %d: %v", p, effort, err)
			}
			t0 = time.Now()
			back, err := DecodeJPEG(j)
			dec += time.Since(t0)
			if err != nil {
				t.Fatalf("dec %s: %v", p, err)
			}
			if string(back) != string(src) {
				t.Fatalf("mismatch %s", p)
			}
			in += int64(len(src))
			out += int64(len(j))
		}
		t.Logf("effort %d: ratio %.1f%%  enc %v  dec %v  (%d files)",
			effort, 100*float64(out)/float64(in), enc.Round(time.Millisecond), dec.Round(time.Millisecond), len(entries))
	}
}
