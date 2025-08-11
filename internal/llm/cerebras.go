package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type CerebrasClient struct {
	HTTPClient *http.Client
	APIKey     string
	Model      string
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionsRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	FinishReason string      `json:"finish_reason"`
	Message      chatMessage `json:"message"`
}

type chatCompletionsResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
}

func NewCerebrasClient(apiKey, model string) *CerebrasClient {
	return &CerebrasClient{
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
		APIKey:     apiKey,
		Model:      model,
	}
}

func (c *CerebrasClient) Generate(ctx context.Context, prompt string) (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("cerebras api key missing")
	}
	endpoint := "https://api.cerebras.ai/v1/chat/completions"

	messages := []chatMessage{
		{Role: "system", Content: "You are a helpful, concise voice AI agent. Answer clearly and briefly."},
		{Role: "user", Content: prompt},
	}

	reqBody, _ := json.Marshal(chatCompletionsRequest{Model: c.Model, Messages: messages})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("cerebras error: status=%d body=%s", resp.StatusCode, string(b))
	}
	var cr chatCompletionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", err
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("cerebras: empty choices")
	}
	answer := cr.Choices[0].Message.Content
	return strings.TrimSpace(answer), nil
}
