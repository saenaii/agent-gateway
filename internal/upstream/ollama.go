// Package upstream provides a minimal client for Ollama's chat API, used by
// Layer 1 intent classification.
package upstream

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

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest mirrors the subset of the Ollama chat request we send.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Format   string    `json:"format,omitempty"`
	Options  Options   `json:"options,omitempty"`
}

// Options holds sampling options.
type Options struct {
	Temperature float64 `json:"temperature,omitempty"`
}

// Usage reports token counts returned by the upstream model.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
}

// ChatResponse is the parsed Ollama chat reply.
type ChatResponse struct {
	Content string
	Usage   Usage
}

type chatReply struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	PromptEvalCount int64  `json:"prompt_eval_count"`
	EvalCount       int64  `json:"eval_count"`
	Error           string `json:"error"`
}

// Client talks to an Ollama instance.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a Client for the given Ollama base URL and timeout.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// Chat runs one non-streaming chat completion and returns the reply.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if req.Stream {
		return nil, fmt.Errorf("streaming chat not supported by Chat")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post %s: %w", c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read chat response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama status %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var reply chatReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("decode chat response: %w", err)
	}
	if reply.Error != "" {
		return nil, fmt.Errorf("ollama error: %s", reply.Error)
	}
	return &ChatResponse{
		Content: reply.Message.Content,
		Usage: Usage{
			PromptTokens:     reply.PromptEvalCount,
			CompletionTokens: reply.EvalCount,
		},
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
