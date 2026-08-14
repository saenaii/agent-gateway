package layer2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-gateway/internal/upstream"
)

// fakeOpenCode mimics the opencode serve HTTP API: session create, message
// send, and session delete.
type fakeOpenCode struct {
	mu            sync.Mutex
	reply         string
	messageStatus int
	sessions      map[string]bool
	created       int
	deleted       int
	lastBody      map[string]any
}

func newFakeOpenCode() *fakeOpenCode {
	return &fakeOpenCode{
		sessions:      make(map[string]bool),
		messageStatus: http.StatusOK,
	}
}

func (f *fakeOpenCode) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			f.created++
			id := "sess-1"
			f.sessions[id] = true
			writeJSON(w, http.StatusOK, map[string]any{"id": id})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/message"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastBody = body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.messageStatus)
			_, _ = w.Write([]byte(f.reply))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/session/"):
			f.deleted++
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
		default:
			http.NotFound(w, r)
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeOpenCode) requestCounts() (created, deleted int, lastBody map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created, f.deleted, f.lastBody
}

func newOpenCodeForTest(t *testing.T, fake *fakeOpenCode) *OpenCode {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	p, err := NewOpenCode(OpenCodeConfig{BaseURL: server.URL, Model: "opencode/deepseek-v4-flash-free", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func replyBody(content string, input, output int64) string {
	parts := []map[string]any{{"type": "text", "text": content}}
	b, _ := json.Marshal(map[string]any{
		"info": map[string]any{
			"tokens": map[string]any{"input": input, "output": output},
		},
		"parts": parts,
	})
	return string(b)
}

func TestOpenCodeChat(t *testing.T) {
	fake := newFakeOpenCode()
	fake.reply = replyBody("quantum answer", 100, 50)
	p := newOpenCodeForTest(t, fake)

	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []upstream.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "explain"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "quantum answer" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CompletionTokens != 50 {
		t.Errorf("usage = %+v", resp.Usage)
	}

	created, deleted, body := fake.requestCounts()
	if created != 1 || deleted != 1 {
		t.Errorf("sessions created/deleted = %d/%d, want 1/1", created, deleted)
	}
	model := body["model"].(map[string]any)
	if model["providerID"] != "opencode" || model["modelID"] != "deepseek-v4-flash-free" {
		t.Errorf("model = %+v", model)
	}
	if body["system"] != "You are helpful." {
		t.Errorf("system = %q", body["system"])
	}
	parts := body["parts"].([]any)
	first := parts[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "explain" {
		t.Errorf("parts = %+v", first)
	}
}

func TestOpenCodeChatMultiTurnPrompt(t *testing.T) {
	fake := newFakeOpenCode()
	fake.reply = replyBody("answer", 1, 1)
	p := newOpenCodeForTest(t, fake)

	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []upstream.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "user", Content: "explain"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, _, body := fake.requestCounts()
	parts := body["parts"].([]any)
	text := parts[0].(map[string]any)["text"].(string)
	want := "user: hi\nassistant: hello\nuser: explain"
	if text != want {
		t.Errorf("prompt = %q, want %q", text, want)
	}
}

func TestOpenCodeChatSkipsSyntheticParts(t *testing.T) {
	fake := newFakeOpenCode()
	b, _ := json.Marshal(map[string]any{
		"info": map[string]any{"tokens": map[string]any{"input": 1, "output": 2}},
		"parts": []map[string]any{
			{"type": "text", "text": "real answer"},
			{"type": "text", "text": "synthetic filler", "synthetic": true},
			{"type": "tool", "state": map[string]any{"status": "completed"}},
			{"type": "text", "text": "ignored note", "ignored": true},
		},
	})
	fake.reply = string(b)
	p := newOpenCodeForTest(t, fake)

	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []upstream.Message{{Role: "user", Content: "q"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "real answer" {
		t.Errorf("content = %q, want only the real answer", resp.Content)
	}
}

func TestOpenCodeChatUpstreamError(t *testing.T) {
	fake := newFakeOpenCode()
	fake.reply = `{"info":{"error":{"name":"APIError","data":{"message":"model overloaded"}}}}`
	p := newOpenCodeForTest(t, fake)

	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []upstream.Message{{Role: "user", Content: "q"}},
	}); err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("err = %v, want model overloaded", err)
	}
}

func TestOpenCodeChatHTTPError(t *testing.T) {
	fake := newFakeOpenCode()
	fake.reply = `{"name":"BadRequest","data":{"message":"bad model"}}`
	fake.messageStatus = http.StatusBadRequest
	p := newOpenCodeForTest(t, fake)

	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []upstream.Message{{Role: "user", Content: "q"}},
	})
	if err == nil || !strings.Contains(err.Error(), "bad model") {
		t.Fatalf("err = %v, want bad model", err)
	}
}

func TestOpenCodeChatEmptyMessages(t *testing.T) {
	fake := newFakeOpenCode()
	fake.reply = replyBody("x", 1, 1)
	p := newOpenCodeForTest(t, fake)
	if _, err := p.Chat(context.Background(), ChatRequest{}); err == nil {
		t.Fatal("expected error for empty messages")
	}
}

func TestNewOpenCodeValidation(t *testing.T) {
	if _, err := NewOpenCode(OpenCodeConfig{BaseURL: "http://x", Model: ""}); err == nil {
		t.Error("expected error for empty model")
	}
	if _, err := NewOpenCode(OpenCodeConfig{BaseURL: "", Model: "deepseek-v4"}); err == nil {
		t.Error("expected error for empty base url")
	}
}

func TestNewOpenCodeBareModelDefaultsToOpencodeProvider(t *testing.T) {
	p, err := NewOpenCode(OpenCodeConfig{BaseURL: "http://x", Model: "deepseek-v4-flash", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if p.model.ProviderID != "opencode" || p.model.ModelID != "deepseek-v4-flash" {
		t.Errorf("model = %+v", p.model)
	}
}
