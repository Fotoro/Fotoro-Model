package ollama

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const SystemPrompt = "You are a precise visual analyst. Describe personal photos accurately in 2-3 dense sentences. State the main event, setting, and specific details including people's clothing, facial expressions, and notable objects. Be direct. Do not start with 'The image shows' or 'In this picture'."

type Client struct {
	addr       string
	model      string
	httpClient *http.Client
	serverCmd  *exec.Cmd
	started    bool
	mu         sync.Mutex
	transport  *http.Transport
}

type Analysis struct {
	Caption     string
	Category    string
	Tags        []string
	HasText     bool
	HasFaces    bool
	Orientation string
}

func NewClient(baseURL, model string) *Client {
	addr := os.Getenv("LLAMA_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:8081"
	}
	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     120 * time.Second,
	}
	return &Client{
		addr:       addr,
		model:      model,
		httpClient: &http.Client{Timeout: 120 * time.Second, Transport: transport},
		transport:  transport,
	}
}

func (c *Client) lazyStart() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	modelPath := os.Getenv("FOTORO_MODEL_PATH")
	mmprojPath := os.Getenv("FOTORO_MMPROJ_PATH")
	if modelPath == "" || mmprojPath == "" {
		return fmt.Errorf("set FOTORO_MODEL_PATH and FOTORO_MMPROJ_PATH env vars")
	}
	if err := c.startServer(modelPath, mmprojPath); err != nil {
		return err
	}
	c.started = true
	return nil
}

func (c *Client) startServer(modelPath, mmprojPath string) error {
	if c.healthCheck() == nil {
		return nil
	}

	exe := os.Getenv("LLAMA_SERVER_BIN")
	if exe == "" {
		for _, path := range []string{
			"./llama-server",
			"./llama.cpp/build/bin/llama-server",
			"./llama.cpp/build/llama-server",
			"../llama.cpp/build/bin/llama-server",
			"/usr/local/bin/llama-server",
		} {
			if _, err := os.Stat(path); err == nil {
				exe = path
				break
			}
		}
		if exe == "" {
			exe = "llama-server"
		}
	}

	threads := getEnv("LLAMA_THREADS", "4")
	ctxSize := getEnv("LLAMA_CTX_SIZE", "2048")

	addr := strings.TrimPrefix(c.addr, "http://")
	host := addr
	port := "8081"
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		host = addr[:idx]
		port = addr[idx+1:]
	}

	args := []string{
		"-m", modelPath,
		"--mmproj", mmprojPath,
		"--host", host,
		"--port", port,
		"--threads", threads,
		"--ctx-size", ctxSize,
		"--batch-size", "1024",
		"--ubatch-size", "1024",
		"--cache-type-k", "q8_0",
		"--cache-type-v", "q8_0",
		"--mlock",
		"--no-mmap",
		"--cache-prompt",
		"--image-min-tokens", "128",
		"--image-max-tokens", "512",
	}
	if os.Getenv("LLAMA_FLASH_ATTN") == "1" {
		args = append(args, "--flash-attn", "on")
	}

	c.serverCmd = exec.Command(exe, args...)
	c.serverCmd.Stdout = os.Stdout
	c.serverCmd.Stderr = os.Stderr
	c.serverCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	fmt.Printf("[LLM] Starting llama-server (3B): %s %v\\n", exe, args)
	if err := c.serverCmd.Start(); err != nil {
		return fmt.Errorf("start llama-server: %w", err)
	}

	for i := 0; i < 100; i++ {
		if c.healthCheck() == nil {
			fmt.Println("[LLM] 3B Server ready")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("llama-server failed to start")
}

func (c *Client) StopServer() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.serverCmd != nil && c.serverCmd.Process != nil {
		c.serverCmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(500 * time.Millisecond)
		c.serverCmd.Process.Kill()
	}
}

func (c *Client) healthCheck() error {
	resp, err := c.httpClient.Get(c.addr + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) HealthCheck() error {
	return c.lazyStart()
}

func (c *Client) VerifyModel() error {
	return c.lazyStart()
}

// ORIGINAL sampling params that produced good captions
func (c *Client) AnalyzeImage(vlmBytes []byte) (Analysis, error) {
	if err := c.lazyStart(); err != nil {
		return Analysis{}, err
	}

	b64 := base64.StdEncoding.EncodeToString(vlmBytes)

	reqBody := map[string]interface{}{
		"model": c.model,
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": SystemPrompt,
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "image_url",
						"image_url": map[string]string{
							"url": "data:image/jpeg;base64," + b64,
						},
					},
					{
						"type": "text",
						"text": "Describe this photo concisely.",
					},
				},
			},
		},
		"max_tokens":  128,
		"temperature": 0.2,
		"stop":        []string{"USER:", "ASSISTANT:", "</s>", "|im_end|"},
		"cache_prompt": true,
	}

	body, _ := json.Marshal(reqBody)
	resp, err := c.httpClient.Post(c.addr+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		return Analysis{}, err
	}
	defer resp.Body.Close()

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Analysis{}, err
	}
	if len(result.Choices) == 0 {
		return Analysis{}, fmt.Errorf("no response")
	}

	raw := result.Choices[0].Message.Content
	analysis := parseAnalysis(raw)
	analysis.Caption = addPrefix(analysis.Caption, analysis.Category)
	
	return analysis, nil
}

// Binary prefix: Photo vs Document only
func addPrefix(caption, category string) string {
	if category == "documents" {
		return "Document: " + caption
	}
	return "Photo: " + caption
}

func (c *Client) GetEmbedding(text string) ([]float32, error) {
	return nil, fmt.Errorf("GetEmbedding disabled: use EMBED_ADDR with a dedicated embed model")
}

func (c *Client) ExpandQuery(q string) string {
	return q
}

// Binary classification: photo vs document
func parseAnalysis(raw string) Analysis {
	a := Analysis{Category: "photo", Orientation: "landscape"}
	lines := strings.Split(raw, "\\n")
	if len(lines) > 0 {
		a.Caption = strings.TrimSpace(lines[0])
	}
	lower := strings.ToLower(raw)

	switch {
	case strings.Contains(lower, "document") || strings.Contains(lower, "text") ||
		 strings.Contains(lower, "receipt") || strings.Contains(lower, "paper") ||
		 strings.Contains(lower, "handwriting") || strings.Contains(lower, "handwritten") ||
		 strings.Contains(lower, "notes") || strings.Contains(lower, "notebook") ||
		 strings.Contains(lower, "letter") || strings.Contains(lower, "form") ||
		 strings.Contains(lower, "invoice") || strings.Contains(lower, "bill") ||
		 strings.Contains(lower, "page") || strings.Contains(lower, "writing") ||
		 strings.Contains(lower, "printed") || strings.Contains(lower, "sign") ||
		 strings.Contains(lower, "label") || strings.Contains(lower, "ticket") ||
		 strings.Contains(lower, "certificate") || strings.Contains(lower, "contract") ||
		 strings.Contains(lower, "menu") || strings.Contains(lower, "receipt"):
		a.Category = "documents"
		a.HasText = true
	default:
		a.Category = "photo"
	}

	re := regexp.MustCompile(`\\b\\w{4,}\\b`)
	words := re.FindAllString(lower, -1)
	seen := make(map[string]bool)
	for _, w := range words {
		if !isStopWord(w) {
			seen[w] = true
		}
	}
	for t := range seen {
		a.Tags = append(a.Tags, t)
	}
	return a
}

func isStopWord(w string) bool {
	switch w {
	case "the", "and", "this", "that", "with", "from", "image", "photo", "picture",
		 "shows", "showing", "depicts", "features", "displays":
		return true
	}
	return false
}

func getEnv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}