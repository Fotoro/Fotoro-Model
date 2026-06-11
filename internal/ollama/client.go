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

const SystemPrompt = `You are a precise visual analyst. Describe images exactly as they appear. Be factual and specific. Never invent details.`

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
	ImageType   string
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
	ctxSize := getEnv("LLAMA_CTX_SIZE", "1024")

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
		"--batch-size", "512",
		"--ubatch-size", "512",
		"--cache-type-k", "q8_0",
		"--cache-type-v", "q8_0",
		"--image-min-tokens", "64",
		"--image-max-tokens", "256",
		"--no-mmap",
	}

	if os.Getenv("LLAMA_MLOCK") == "1" {
		args = append(args, "--mlock")
	}

	if os.Getenv("LLAMA_FLASH_ATTN") == "1" {
		args = append(args, "--flash-attn")
	}

	c.serverCmd = exec.Command(exe, args...)
	c.serverCmd.Stdout = os.Stdout
	c.serverCmd.Stderr = os.Stderr
	c.serverCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	fmt.Printf("[LLM] Starting llama-server (3B CPU-optimized): %s %v\n", exe, args)
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

func (c *Client) AnalyzeImage(vlmBytes []byte) (Analysis, error) {
	if err := c.lazyStart(); err != nil {
		return Analysis{}, err
	}

	b64 := base64.StdEncoding.EncodeToString(vlmBytes)

	// SPEED OPTIMIZED PROMPT:
	// - No type classification in output (saves ~5-10s)
	// - Direct description only
	// - Very strict length limit
	// - Lower temp for less wandering
	userPrompt := `Describe this image in 2 concise sentences. Be specific and factual.

Rules:
- Start directly with the subject. NEVER say "The image shows" or "This is a photo of"
- For screenshots: describe the app, visible text, UI elements
- For documents: transcribe key text, describe layout
- For wallpapers: describe colors, pattern, subject, style
- For photos: describe people, clothing, setting, objects, action, event
- For artwork: describe style, subject, colors, medium
- NEVER invent details. If unclear, describe what you can see.
- Be dense and specific.`

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
						"text": userPrompt,
					},
				},
			},
		},
		"max_tokens":  96,        // STRICT: ~50 words max
		"temperature": 0.05,      // NEAR-DETERMINISTIC: barely any creativity
		"stop":        []string{"USER:", "ASSISTANT:", "</s>", "|im_end|", "\n"},
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

	return analysis, nil
}

func (c *Client) GetEmbedding(text string) ([]float32, error) {
	return nil, fmt.Errorf("GetEmbedding disabled: use EMBED_ADDR with a dedicated embed model")
}

func (c *Client) ExpandQuery(q string) string {
	return q
}

func parseAnalysis(raw string) Analysis {
	a := Analysis{Category: "unknown", Orientation: "landscape", ImageType: "photo"}

	// Clean up the raw caption
	raw = strings.TrimSpace(raw)

	// Remove common prefixes the model might still generate despite instructions
	prefixes := []string{
		"the image shows ", "this is a photo of ", "this image depicts ",
		"the photo shows ", "this is a screenshot of ", "this is a picture of ",
		"the image is ", "this image is ", "the photo is ", "this photo is ",
	}
	lowerRaw := strings.ToLower(raw)
	for _, p := range prefixes {
		if strings.HasPrefix(lowerRaw, p) {
			raw = raw[len(p):]
			break
		}
	}

	// Capitalize first letter
	if len(raw) > 0 {
		raw = strings.ToUpper(raw[:1]) + raw[1:]
	}

	a.Caption = raw

	lower := strings.ToLower(a.Caption)

	// Detect category from caption content (internal only, not in output)
	switch {
	case strings.Contains(lower, "screenshot") || strings.Contains(lower, "screen") || strings.Contains(lower, "interface") || strings.Contains(lower, "app ") || strings.Contains(lower, "whatsapp") || strings.Contains(lower, "chat") || strings.Contains(lower, "conversation"):
		a.ImageType = "screenshot"
		a.Category = "screenshots"
	case strings.Contains(lower, "document") || strings.Contains(lower, "receipt") || strings.Contains(lower, "paper") || strings.Contains(lower, "invoice") || strings.Contains(lower, "text") || strings.Contains(lower, "form") || strings.Contains(lower, "page"):
		a.ImageType = "document"
		a.Category = "documents"
		a.HasText = true
	case strings.Contains(lower, "meme"):
		a.ImageType = "meme"
		a.Category = "memes"
	case strings.Contains(lower, "artwork") || strings.Contains(lower, "painting") || strings.Contains(lower, "digital art") || strings.Contains(lower, "illustration"):
		a.ImageType = "artwork"
		a.Category = "art"
	case strings.Contains(lower, "icon") || strings.Contains(lower, "logo"):
		a.ImageType = "icon"
		a.Category = "icons"
	case strings.Contains(lower, "wallpaper") || strings.Contains(lower, "pattern") || strings.Contains(lower, "abstract") || strings.Contains(lower, "gradient") || strings.Contains(lower, "texture"):
		a.ImageType = "wallpaper"
		a.Category = "wallpapers"
	case strings.Contains(lower, "person") || strings.Contains(lower, "face") || strings.Contains(lower, "portrait") || strings.Contains(lower, "man") || strings.Contains(lower, "woman") || strings.Contains(lower, "people") || strings.Contains(lower, "child") || strings.Contains(lower, "group"):
		a.ImageType = "photo"
		a.Category = "people"
		a.HasFaces = true
	case strings.Contains(lower, "landscape") || strings.Contains(lower, "mountain") || strings.Contains(lower, "beach") || strings.Contains(lower, "nature") || strings.Contains(lower, "sky") || strings.Contains(lower, "forest") || strings.Contains(lower, "ocean") || strings.Contains(lower, "sunset"):
		a.ImageType = "photo"
		a.Category = "landscape"
	case strings.Contains(lower, "building") || strings.Contains(lower, "architecture") || strings.Contains(lower, "house") || strings.Contains(lower, "city") || strings.Contains(lower, "street"):
		a.ImageType = "photo"
		a.Category = "architecture"
	case strings.Contains(lower, "animal") || strings.Contains(lower, "dog") || strings.Contains(lower, "cat") || strings.Contains(lower, "bird") || strings.Contains(lower, "pet"):
		a.ImageType = "photo"
		a.Category = "animals"
	case strings.Contains(lower, "food") || strings.Contains(lower, "meal") || strings.Contains(lower, "dish") || strings.Contains(lower, "cuisine") || strings.Contains(lower, "restaurant"):
		a.ImageType = "photo"
		a.Category = "food"
	default:
		a.ImageType = "photo"
	}

	// Extract tags
	re := regexp.MustCompile(`\b\w{4,}\b`)
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
		 "shows", "showing", "depicts", "features", "displays", "appears", "seems",
		 "looks", "like", "about", "some", "very", "also", "have", "been", "there",
		 "their", "they", "them", "than", "then", "when", "where", "what", "which",
		 "who", "will", "would", "could", "should", "may", "might", "must", "can",
		 "into", "over", "under", "above", "below", "between", "through", "during",
		 "before", "after", "while", "because", "since", "until", "although",
		 "however", "therefore", "furthermore", "moreover", "nevertheless":
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