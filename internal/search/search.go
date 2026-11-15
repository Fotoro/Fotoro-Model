package search

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"math"
	"runtime"
	"sort"
	"sync"
	"time"
)

type Vector struct {
	ID       int
	Path     string
	Hash     string
	Caption  string
	Category string
	Data     []float32
}

type Index struct {
	mu       sync.RWMutex
	vectors  []Vector
	norms    []float32
	byHash   map[string]int
	lastLoad time.Time
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
	byHash := make(map[string]int)
	for rows.Next() {
		var v Vector
		var blob []byte
		if err := rows.Scan(&v.ID, &v.Path, &v.Hash, &v.Caption, &v.Category, &blob); err != nil {
			continue
		}
		v.Data = bytesToFloats(blob)
		if len(v.Data) > 0 {
			byHash[v.Hash] = len(vecs)
			vecs = append(vecs, v)
			norms = append(norms, vectorNorm(v.Data))
		}
	}

	idx.mu.Lock()
	idx.vectors = vecs
	idx.norms = norms
	idx.byHash = byHash
	idx.lastLoad = time.Now()
	idx.mu.Unlock()
	return nil
}

// LoadNew incrementally merges rows embedded since the index's last load,
// replacing vectors for hashes already present.
func (idx *Index) LoadNew(db *sql.DB) (int, error) {
	idx.mu.RLock()
	since := idx.lastLoad
	idx.mu.RUnlock()

	rows, err := db.Query(`
		SELECT id, path, hash, caption, category, embedding FROM images
		WHERE embedding IS NOT NULL AND embedded_at > ?`, since)
	if err != nil {
		// Older schemas lack embedded_at; fall back to a full reload.
		return 0, idx.LoadFromDB(db)
	}
	defer rows.Close()

	var fresh []Vector
	for rows.Next() {
		var v Vector
		var blob []byte
		if err := rows.Scan(&v.ID, &v.Path, &v.Hash, &v.Caption, &v.Category, &blob); err != nil {
			continue
		}
		v.Data = bytesToFloats(blob)
		if len(v.Data) > 0 {
			fresh = append(fresh, v)
		}
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.byHash == nil {
		idx.byHash = make(map[string]int, len(idx.vectors))
		for i, v := range idx.vectors {
			idx.byHash[v.Hash] = i
		}
	}
	for _, v := range fresh {
		if slot, ok := idx.byHash[v.Hash]; ok {
			idx.vectors[slot] = v
			idx.norms[slot] = vectorNorm(v.Data)
			continue
		}
		idx.byHash[v.Hash] = len(idx.vectors)
		idx.vectors = append(idx.vectors, v)
		idx.norms = append(idx.norms, vectorNorm(v.Data))
	}
	idx.lastLoad = time.Now()
	return len(fresh), nil
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

	workers := runtime.GOMAXPROCS(0)
	if workers > len(vecs) || workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8
	}
	per := make([][]Result, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		start := w * len(vecs) / workers
		end := (w + 1) * len(vecs) / workers
		wg.Add(1)
		go func(dst *[]Result, lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				score := fastCosine(query, vecs[i].Data, norms[i]*qNorm)
				if score > 0.10 {
					*dst = append(*dst, Result{vecs[i], score})
				}
			}
		}(&per[w], start, end)
	}
	wg.Wait()

	var scored []Result
	for _, part := range per {
		scored = append(scored, part...)
	}
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
	defer idx.mu.Unlock()
	if idx.byHash != nil {
		if slot, ok := idx.byHash[v.Hash]; ok {
			idx.vectors[slot] = v
			idx.norms[slot] = vectorNorm(v.Data)
			return
		}
		idx.byHash[v.Hash] = len(idx.vectors)
	}
	idx.vectors = append(idx.vectors, v)
	idx.norms = append(idx.norms, vectorNorm(v.Data))
}

func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.vectors)
}
