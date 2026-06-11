package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// EmbedClient talks to a tiny dedicated embed server (e.g. nomic-embed-text via llama.cpp).
type EmbedClient struct {
	addr      string
	client    *http.Client
	mu        sync.Mutex
	textCache map[string][]float32
}

func NewEmbedClient() *EmbedClient {
	addr := os.Getenv("EMBED_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:8082"
	}
	return &EmbedClient{
		addr:      addr,
		client:    &http.Client{Timeout: 30 * time.Second},
		textCache: make(map[string][]float32),
	}
}

func (c *EmbedClient) HealthCheck() error {
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

// GetEmbedding returns text embedding with simple caching
func (c *EmbedClient) GetEmbedding(text string) ([]float32, error) {
	c.mu.Lock()
	if emb, ok := c.textCache[text]; ok {
		c.mu.Unlock()
		return emb, nil
	}
	c.mu.Unlock()

	req := map[string]interface{}{
		"input": text,
	}
	body, _ := json.Marshal(req)
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

	emb := result.Data[0].Embedding
	c.mu.Lock()
	c.textCache[text] = emb
	c.mu.Unlock()
	return emb, nil
}