package layer2

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"agent-gateway/internal/upstream"
)

type stubProvider struct {
	name     string
	failures int32 // remaining failures before success
	attempts int32 // total Chat calls
	content  string
	err      error
}

func (s *stubProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	atomic.AddInt32(&s.attempts, 1)
	if s.err != nil {
		return nil, s.err
	}
	if n := atomic.AddInt32(&s.failures, -1); n >= 0 {
		return nil, errors.New("stub upstream failure")
	}
	return &ChatResponse{Content: s.content, Usage: upstream.Usage{PromptTokens: 10, CompletionTokens: 20}}, nil
}

func chatReq() ChatRequest {
	return ChatRequest{Messages: []upstream.Message{{Role: "user", Content: "hi"}}}
}

func TestRouterChatUsesDefaultForUnknownName(t *testing.T) {
	r := NewRouter("opencode", 0, 3, time.Minute)
	def := &stubProvider{name: "opencode", content: "default answer"}
	other := &stubProvider{name: "other", content: "other answer"}
	r.Add("opencode", def)
	r.Add("other", other)

	resp, err := r.Chat(context.Background(), "missing", chatReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "default answer" {
		t.Errorf("content = %q, want default answer", resp.Content)
	}
	if atomic.LoadInt32(&other.attempts) != 0 {
		t.Error("named provider should not be called for unknown name")
	}
}

func TestRouterChatSelectsNamedProvider(t *testing.T) {
	r := NewRouter("opencode", 0, 3, time.Minute)
	def := &stubProvider{name: "opencode", content: "default answer"}
	other := &stubProvider{name: "other", content: "other answer"}
	r.Add("opencode", def)
	r.Add("other", other)

	resp, err := r.Chat(context.Background(), "other", chatReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "other answer" {
		t.Errorf("content = %q, want other answer", resp.Content)
	}
	if atomic.LoadInt32(&def.attempts) != 0 {
		t.Error("default provider should not be called when named provider exists")
	}
}

func TestRouterChatRetriesThenSucceeds(t *testing.T) {
	r := NewRouter("opencode", 2, 3, time.Minute)
	p := &stubProvider{name: "opencode", failures: 2, content: "recovered"}
	r.Add("opencode", p)

	resp, err := r.Chat(context.Background(), "", chatReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "recovered" {
		t.Errorf("content = %q, want recovered", resp.Content)
	}
	if got := atomic.LoadInt32(&p.attempts); got != 3 {
		t.Errorf("attempts = %d, want 3 (1 call + 2 retries)", got)
	}
}

func TestRouterChatExhaustsRetries(t *testing.T) {
	r := NewRouter("opencode", 1, 3, time.Minute)
	p := &stubProvider{name: "opencode", err: errors.New("boom")}
	r.Add("opencode", p)

	if _, err := r.Chat(context.Background(), "", chatReq()); err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if got := atomic.LoadInt32(&p.attempts); got != 2 {
		t.Errorf("attempts = %d, want 2 (1 call + 1 retry)", got)
	}
}

func TestRouterChatBreakerTripsOpen(t *testing.T) {
	r := NewRouter("opencode", 0, 1, time.Minute)
	p := &stubProvider{name: "opencode", err: errors.New("boom")}
	r.Add("opencode", p)

	if _, err := r.Chat(context.Background(), "", chatReq()); err == nil {
		t.Fatal("expected error from failing provider")
	}
	if _, err := r.Chat(context.Background(), "", chatReq()); err == nil {
		t.Fatal("expected circuit breaker open error")
	}
	if got := atomic.LoadInt32(&p.attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 (breaker must block second call)", got)
	}
}

func TestRouterChatNoProvider(t *testing.T) {
	r := NewRouter("opencode", 0, 3, time.Minute)
	if _, err := r.Chat(context.Background(), "", chatReq()); err == nil {
		t.Fatal("expected error when no provider is registered")
	}
}

func TestRouterChatUsage(t *testing.T) {
	r := NewRouter("opencode", 0, 3, time.Minute)
	p := &stubProvider{name: "opencode", content: "answer"}
	r.Add("opencode", p)

	resp, err := r.Chat(context.Background(), "", chatReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 20 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}
