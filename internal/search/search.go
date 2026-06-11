package search

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"math"
	"sort"
	"sync"
)

type Vector struct {
	ID       int
	Path     string
	Hash     string
	Caption  string
	Category string
	Data     []float32
}

// Index holds all embeddings in memory for fast cosine search.
// For 3k images × 768 dims × 4 bytes = ~9MB RAM. Trivial.
type Index struct {
	mu      sync.RWMutex
	vectors []Vector
	// Precomputed norms for speed
	norms []float32
}

func NewIndex() *Index {
	return &Index{}
}

func bytesToFloats(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	count := len(b) / 4
	out := make([]float32, count)
	buf := bytes.NewReader(b)
	for i := 0; i < count; i++ {
		binary.Read(buf, binary.LittleEndian, &out[i])
	}
	return out
}

func FloatsToBytes(f []float32) []byte {
	buf := new(bytes.Buffer)
	for _, v := range f {
		binary.Write(buf, binary.LittleEndian, v)
	}
	return buf.Bytes()
}

// fastCosine uses precomputed norms for O(n) instead of O(3n)
func fastCosine(a []float32, b []float32, bNorm float32) float32 {
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	if bNorm == 0 {
		return -1
	}
	return dot / bNorm
}

func vectorNorm(a []float32) float32 {
	var sum float32
	for _, v := range a {
		sum += v * v
	}
	return float32(math.Sqrt(float64(sum)))
}

func (idx *Index) LoadFromDB(db *sql.DB) error {
	rows, err := db.Query("SELECT id, path, hash, caption, category, embedding FROM images WHERE embedding IS NOT NULL")
	if err != nil {
		return err
	}
	defer rows.Close()

	var vecs []Vector
	var norms []float32
	for rows.Next() {
		var v Vector
		var blob []byte
		if err := rows.Scan(&v.ID, &v.Path, &v.Hash, &v.Caption, &v.Category, &blob); err != nil {
			continue
		}
		v.Data = bytesToFloats(blob)
		if len(v.Data) > 0 {
			vecs = append(vecs, v)
			norms = append(norms, vectorNorm(v.Data))
		}
	}

	idx.mu.Lock()
	idx.vectors = vecs
	idx.norms = norms
	idx.mu.Unlock()
	return nil
}

type Result struct {
	Vector
	Score float32
}

func (idx *Index) Search(query []float32, topN int) []Result {
	qNorm := vectorNorm(query)
	if qNorm == 0 {
		return nil
	}

	idx.mu.RLock()
	vecs := make([]Vector, len(idx.vectors))
	copy(vecs, idx.vectors)
	norms := make([]float32, len(idx.norms))
	copy(norms, idx.norms)
	idx.mu.RUnlock()

	// Score all vectors
	scored := make([]Result, 0, len(vecs))
	for i, v := range vecs {
		score := fastCosine(query, v.Data, norms[i]*qNorm)
		if score > 0.25 {
			scored = append(scored, Result{v, score})
		}
	}

	// Sort by score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	if len(scored) > topN {
		scored = scored[:topN]
	}
	return scored
}

func (idx *Index) Add(v Vector) {
	idx.mu.Lock()
	idx.vectors = append(idx.vectors, v)
	idx.norms = append(idx.norms, vectorNorm(v.Data))
	idx.mu.Unlock()
}