package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// EmbeddingGemma prompt templates. The model is trained for asymmetric
// retrieval: queries and documents use different task prompts.
const (
	embedQueryPrompt = "task: search result | query: "
	embedDocPrompt   = "title: none | text: "
)

// EmbedClient produces embeddings through the Ollama daemon (embeddinggemma
// by default) or an OpenAI-compatible /v1/embeddings server when EMBED_ADDR
// is set. Matryoshka truncation shrinks vectors to EMBED_DIM (default 256).
type EmbedClient struct {
	addr       string
	ollamaAddr string
	model      string
	dim        int
	client     *http.Client
	mu         sync.Mutex
	textCache  map[string][]float32
}

func NewEmbedClient() *EmbedClient {
	c := &EmbedClient{
		addr:       os.Getenv("EMBED_ADDR"),
		ollamaAddr: envDefault("OLLAMA_ADDR", "http://127.0.0.1:11434"),
		model:      envDefault("EMBED_MODEL", "embeddinggemma:latest"),
		client:     &http.Client{Timeout: 60 * time.Second},
		textCache:  make(map[string][]float32),
	}
	if v, err := strconv.Atoi(os.Getenv("EMBED_DIM")); err == nil && v > 0 {
		c.dim = v
	} else {
		c.dim = 256
	}
	return c
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (c *EmbedClient) Model() string { return c.model }
func (c *EmbedClient) Dim() int      { return c.dim }

// usingOllama reports whether requests go to the Ollama daemon rather than
// an external OpenAI-compatible endpoint.
func (c *EmbedClient) usingOllama() bool { return c.addr == "" }

func (c *EmbedClient) HealthCheck() error {
	if c.usingOllama() {
		resp, err := c.client.Get(c.ollamaAddr + "/api/tags")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("ollama status %d", resp.StatusCode)
		}
		return nil
	}
	resp, err := c.client.Get(c.addr + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// EmbedQuery embeds a retrieval query.
func (c *EmbedClient) EmbedQuery(text string) ([]float32, error) {
	return c.embed(embedQueryPrompt + text)
}

// EmbedDocument embeds text for storage in the index.
func (c *EmbedClient) EmbedDocument(text string) ([]float32, error) {
	return c.embed(embedDocPrompt + text)
}

// GetEmbedding keeps the legacy signature; it embeds document text.
func (c *EmbedClient) GetEmbedding(text string) ([]float32, error) {
	return c.EmbedDocument(text)
}

// Truncate applies Matryoshka dimensionality reduction: keep the leading
// dims and renormalize to unit length.
func Truncate(emb []float32, dim int) []float32 {
	if dim <= 0 || dim >= len(emb) {
		return emb
	}
	out := make([]float32, dim)
	copy(out, emb[:dim])
	var sum float64
	for _, v := range out {
		sum += float64(v) * float64(v)
	}
	norm := float32(math.Sqrt(sum))
	if norm == 0 {
		return out
	}
	for i := range out {
		out[i] /= norm
	}
	return out
}

func (c *EmbedClient) embed(text string) ([]float32, error) {
	c.mu.Lock()
	if emb, ok := c.textCache[text]; ok {
		c.mu.Unlock()
		return emb, nil
	}
	c.mu.Unlock()

	var emb []float32
	var err error
	if c.usingOllama() {
		emb, err = c.embedOllama(text)
	} else {
		emb, err = c.embedOpenAI(text)
	}
	if err != nil {
		return nil, err
	}
	emb = Truncate(emb, c.dim)

	c.mu.Lock()
	c.textCache[text] = emb
	c.mu.Unlock()
	return emb, nil
}

func (c *EmbedClient) embedOllama(text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model": c.model,
		"input": text,
	})
	resp, err := c.client.Post(c.ollamaAddr+"/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("ollama: %s", result.Error)
	}
	if len(result.Embeddings) == 0 || len(result.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("no embedding")
	}
	return result.Embeddings[0], nil
}

func (c *EmbedClient) embedOpenAI(text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]interface{}{"input": text})
	resp, err := c.client.Post(c.addr+"/v1/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding")
	}
	return result.Data[0].Embedding, nil
}
