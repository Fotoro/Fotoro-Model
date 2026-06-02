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

const captionPrompt = `Classify and describe this image.

RULES:
1. Start with EXACTLY one label: PHOTO: or SCREENSHOT: or DOCUMENT: or NOTE:
2. After the label, write 1-2 sentences ONLY.
3. If people are visible: describe their clothing color and action.
4. If NO people: describe the main object and setting.
5. If you cannot see a detail clearly, do NOT mention it. Never guess.
6. Do NOT transcribe text, numbers, or conversations.
7. Do NOT invent brand names, locations, or emotions.

EXAMPLES:
PHOTO: A person wearing a red jacket and black pants stands in a grassy park holding a phone.
SCREENSHOT: A mobile phone screen showing a messaging app with a green send button.
DOCUMENT: A printed page with a bar graph and bullet points on white paper.
NOTE: A handwritten page with blue ink and a small diagram in the corner.

Now describe this image:`

// 60 seconds, not 30. CPU under load needs time.
var client = &http.Client{Timeout: 60 * time.Second}

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

func CaptionImage(jpegBytes []byte) (string, error) {
	caption, err := captionOnce(jpegBytes)
	if err != nil {
		// Retry once after 2 seconds
		time.Sleep(2 * time.Second)
		caption, err = captionOnce(jpegBytes)
	}
	return caption, err
}

func captionOnce(jpegBytes []byte) (string, error) {
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
						Text: captionPrompt,
					},
				},
			},
		},
		Temperature: 0.2,
		MaxTokens:   80, // Shorter = faster
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
