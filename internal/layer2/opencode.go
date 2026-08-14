package layer2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-gateway/internal/upstream"
)

// OpenCodeConfig configures the OpenCode provider.
type OpenCodeConfig struct {
	BaseURL string // opencode serve endpoint (e.g. "http://localhost:4096")
	Model   string // "providerID/modelID"; a bare model defaults to the opencode provider
	Timeout time.Duration
}

// OpenCode is a Layer 2 provider backed by an opencode serve instance. Each
// chat call runs in a fresh session: create, prompt, then best-effort delete.
type OpenCode struct {
	baseURL string
	model   modelRef
	client  *http.Client
}

type modelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// NewOpenCode validates the config and returns an OpenCode provider.
func NewOpenCode(cfg OpenCodeConfig) (*OpenCode, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	providerID, modelID, ok := strings.Cut(strings.TrimSpace(cfg.Model), "/")
	if !ok {
		providerID, modelID = "opencode", strings.TrimSpace(cfg.Model)
	}
	if modelID == "" {
		return nil, fmt.Errorf("model is required")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &OpenCode{
		baseURL: baseURL,
		model:   modelRef{ProviderID: providerID, ModelID: modelID},
		client:  &http.Client{Timeout: timeout},
	}, nil
}

// Chat sends the conversation to opencode and returns the assistant text.
func (p *OpenCode) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("opencode: no messages in request")
	}
	system, prompt := buildPrompt(req.Messages)

	sessionID, err := p.createSession(ctx)
	if err != nil {
		return nil, err
	}
	// Best-effort cleanup; a failed delete only leaks a session on the server.
	cleanCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer func() { _ = p.deleteSession(cleanCtx, sessionID) }()

	reply, err := p.sendMessage(ctx, sessionID, system, prompt)
	if err != nil {
		return nil, err
	}
	if reply.Info.Error != nil {
		return nil, fmt.Errorf("opencode: %s", reply.Info.Error.Data.Message)
	}
	return &ChatResponse{
		Content: textContent(reply.Parts),
		Usage: upstream.Usage{
			PromptTokens:     reply.Info.Tokens.Input,
			CompletionTokens: reply.Info.Tokens.Output,
		},
	}, nil
}

func (p *OpenCode) createSession(ctx context.Context) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/session", strings.NewReader("{}"))
	if err != nil {
		return "", fmt.Errorf("opencode: build create session request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	raw, err := p.do(httpReq)
	if err != nil {
		return "", err
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		return "", fmt.Errorf("opencode: decode session response: %w", err)
	}
	if session.ID == "" {
		return "", fmt.Errorf("opencode: session response missing id")
	}
	return session.ID, nil
}

type partInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type sendMessageBody struct {
	Model  modelRef    `json:"model"`
	System string      `json:"system,omitempty"`
	Parts  []partInput `json:"parts"`
}

func (p *OpenCode) sendMessage(ctx context.Context, sessionID, system, prompt string) (*openCodeReply, error) {
	body, err := json.Marshal(sendMessageBody{
		Model:  p.model,
		System: system,
		Parts:  []partInput{{Type: "text", Text: prompt}},
	})
	if err != nil {
		return nil, fmt.Errorf("opencode: marshal message request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/session/"+sessionID+"/message", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("opencode: build message request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	raw, err := p.do(httpReq)
	if err != nil {
		return nil, err
	}
	var reply openCodeReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("opencode: decode message response: %w", err)
	}
	return &reply, nil
}

func (p *OpenCode) deleteSession(ctx context.Context, sessionID string) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, p.baseURL+"/session/"+sessionID, nil)
	if err != nil {
		return err
	}
	_, err = p.do(httpReq)
	return err
}

// do runs one request and returns the response body for 2xx statuses.
func (p *OpenCode) do(req *http.Request) ([]byte, error) {
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opencode: %s %s: %w", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("opencode: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("opencode: status %d: %s", resp.StatusCode, errorMessage(raw))
	}
	return raw, nil
}

func errorMessage(raw []byte) string {
	var named struct {
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &named); err == nil && named.Data.Message != "" {
		return named.Data.Message
	}
	return truncate(string(raw), 200)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type openCodePart struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Synthetic bool   `json:"synthetic"`
	Ignored   bool   `json:"ignored"`
}

type openCodeReply struct {
	Info struct {
		Error *struct {
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
		Tokens struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
		} `json:"tokens"`
	} `json:"info"`
	Parts []openCodePart `json:"parts"`
}

// textContent joins the assistant's text parts, skipping synthetic fillers
// and ignored parts.
func textContent(parts []openCodePart) string {
	var text []string
	for _, part := range parts {
		if part.Type != "text" || part.Synthetic || part.Ignored {
			continue
		}
		text = append(text, part.Text)
	}
	return strings.Join(text, "\n")
}

// buildPrompt splits the conversation into the opencode system prompt and the
// message text. A single user turn is sent verbatim; multi-turn histories are
// flattened to "role: content" lines so opencode sees who said what.
func buildPrompt(messages []upstream.Message) (system, prompt string) {
	var sys []string
	for _, m := range messages {
		if m.Role != "system" {
			continue
		}
		if content := strings.TrimSpace(m.Content); content != "" {
			sys = append(sys, content)
		}
	}
	system = strings.Join(sys, "\n")

	var turns []upstream.Message
	for _, m := range messages {
		if m.Role != "system" {
			turns = append(turns, m)
		}
	}
	if len(turns) == 1 {
		return system, turns[0].Content
	}
	lines := make([]string, 0, len(turns))
	for _, m := range turns {
		lines = append(lines, m.Role+": "+m.Content)
	}
	return system, strings.Join(lines, "\n")
}
