package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const ServerURL = "http://localhost:8080/v1/chat/completions"

var client = &http.Client{Timeout: 30 * time.Second}

type captionPayload struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string            `json:"type"`
			Text     string            `json:"text,omitempty"`
			ImageURL map[string]string `json:"image_url,omitempty"`
		} `json:"content"`
	} `json:"messages"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
}

type captionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// CaptionImage sends a JPEG to llama-server and returns the caption.
// This is the ONLY function that talks to the vision model.
func CaptionImage(jpegBytes []byte) (string, error) {
	b64 := base64.StdEncoding.EncodeToString(jpegBytes)

	payload := captionPayload{
		Model: "smolvlm",
		Messages: []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string            `json:"type"`
				Text     string            `json:"text,omitempty"`
				ImageURL map[string]string `json:"image_url,omitempty"`
			} `json:"content"`
		}{
			{
				Role: "user",
				Content: []struct {
					Type     string            `json:"type"`
					Text     string            `json:"text,omitempty"`
					ImageURL map[string]string `json:"image_url,omitempty"`
				}{
					{
						Type:     "image_url",
						ImageURL: map[string]string{"url": "data:image/jpeg;base64," + b64},
					},
					{
						Type: "text",
						Text: "Describe this image in detail. List: main subject, clothing colors, visible objects, setting, lighting. Be specific. Use exact color names. Write 2-4 sentences.",
					},
				},
			},
		},
		Temperature: 0.2,
		MaxTokens:   100,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	resp, err := client.Post(ServerURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}

	var result captionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices")
	}

	return result.Choices[0].Message.Content, nil
}
