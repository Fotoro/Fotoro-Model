package ollama

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	model   string
	client  *http.Client
}

type ImageAnalysis struct {
	Category    string   `json:"category"`
	Caption     string   `json:"caption"`
	Tags        []string `json:"tags"`
	HasText     bool     `json:"has_text"`
	HasFaces    bool     `json:"has_faces"`
	Orientation string   `json:"orientation"`
}

func NewClient(baseURL, model string) *Client {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	timeout := 300 * time.Second
	if t := os.Getenv("FOTORO_TIMEOUT"); t != "" {
		if d, err := strconv.Atoi(t); err == nil && d > 0 {
			timeout = time.Duration(d) * time.Second
		}
	}
	return &Client{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *Client) HealthCheck() error {
	resp, err := c.client.Get(c.baseURL + "/api/tags")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func baseModelName(fullName string) string {
	if idx := strings.LastIndex(fullName, "/"); idx != -1 {
		fullName = fullName[idx+1:]
	}
	return fullName
}

func (c *Client) VerifyModel() error {
	resp, err := c.client.Get(c.baseURL + "/api/tags")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	var names []string
	for _, m := range result.Models {
		name := baseModelName(m.Name)
		names = append(names, name)
		if name == c.model || strings.HasPrefix(name, c.model+":") {
			return nil
		}
	}
	return fmt.Errorf("model %q not found in Ollama.\nAvailable: %v\n\nTry: export FOTORO_MODEL=%s", c.model, names, names[0])
}

func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// parseKeyValue tries to extract fields from plain text like:
//   Category: photo
//   Caption: A cat sitting...
func parseKeyValue(text string) *ImageAnalysis {
	a := &ImageAnalysis{
		Category:    "unknown",
		Caption:     "",
		Tags:        []string{},
		HasText:     false,
		HasFaces:    false,
		Orientation: "unknown",
	}

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)

		// Category
		if strings.HasPrefix(lower, "category:") {
			a.Category = strings.TrimSpace(strings.TrimPrefix(line, "Category:"))
			a.Category = strings.TrimSpace(strings.TrimPrefix(a.Category, "category:"))
		}
		// Caption
		if strings.HasPrefix(lower, "caption:") {
			a.Caption = strings.TrimSpace(strings.TrimPrefix(line, "Caption:"))
			a.Caption = strings.TrimSpace(strings.TrimPrefix(a.Caption, "caption:"))
		}
		// Tags
		if strings.HasPrefix(lower, "tags:") {
			tagsStr := strings.TrimSpace(strings.TrimPrefix(line, "Tags:"))
			tagsStr = strings.TrimSpace(strings.TrimPrefix(tagsStr, "tags:"))
			for _, t := range strings.Split(tagsStr, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					a.Tags = append(a.Tags, t)
				}
			}
		}
		// HasText
		if strings.HasPrefix(lower, "hastext:") || strings.HasPrefix(lower, "has_text:") {
			v := strings.TrimSpace(line[strings.Index(line, ":")+1:])
			if strings.Contains(strings.ToLower(v), "yes") || strings.Contains(strings.ToLower(v), "true") {
				a.HasText = true
			}
		}
		// HasFaces
		if strings.HasPrefix(lower, "hasfaces:") || strings.HasPrefix(lower, "has_faces:") {
			v := strings.TrimSpace(line[strings.Index(line, ":")+1:])
			if strings.Contains(strings.ToLower(v), "yes") || strings.Contains(strings.ToLower(v), "true") {
				a.HasFaces = true
			}
		}
		// Orientation
		if strings.HasPrefix(lower, "orientation:") {
			a.Orientation = strings.TrimSpace(strings.TrimPrefix(line, "Orientation:"))
			a.Orientation = strings.TrimSpace(strings.TrimPrefix(a.Orientation, "orientation:"))
		}
	}

	// Cleanup
	a.Category = strings.ToLower(strings.TrimSpace(a.Category))
	a.Orientation = strings.ToLower(strings.TrimSpace(a.Orientation))
	if a.Caption == "" {
		// If no caption line found, use the whole text as caption (fallback)
		a.Caption = strings.ReplaceAll(text, "\n", " ")
	}
	return a
}

func (c *Client) AnalyzeImage(jpegBytes []byte) (*ImageAnalysis, error) {
	b64 := base64.StdEncoding.EncodeToString(jpegBytes)

	// Dead simple prompt. No rules, no JSON, no keys. Just describe the image.
	promptText := `Describe the image accurately in 2 to 3 dense sentences. State the main event or action, the setting, and specific details including people's clothing, facial expressions, and notable objects. Be direct. Do not start with "The image shows" or "In this picture".`

	payload := map[string]interface{}{
		"model":  c.model,
		"stream": false,
		"messages": []map[string]interface{}{
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
						"text": promptText,
					},
				},
			},
		},
		"temperature": 0.1, // Gives it just enough room to breathe without hallucinating
		"max_tokens":  60,  // Keep it short!
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		resp, err := c.client.Post(c.baseURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
			continue
		}

		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(respBody, &result); err != nil {
			lastErr = fmt.Errorf("json parse: %w | raw: %s", err, string(respBody))
			continue
		}

		if len(result.Choices) == 0 {
			lastErr = fmt.Errorf("empty choices")
			continue
		}

		content := stripMarkdownFences(result.Choices[0].Message.Content)
		if content == "" {
			lastErr = fmt.Errorf("empty content")
			continue
		}

		// Just shove the entire raw output directly into the caption.
		// Default everything else so the database doesn't complain.
		return &ImageAnalysis{
			Caption:     strings.ReplaceAll(strings.TrimSpace(content), "\n", " "),
			Category:    "unknown",
			Tags:        []string{},
			HasText:     false,
			HasFaces:    false,
			Orientation: "unknown",
		}, nil
	}

	return nil, fmt.Errorf("ollama failed after 3 attempts: %w", lastErr)
}
