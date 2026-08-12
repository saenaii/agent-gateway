package intent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-gateway/internal/breaker"
	"agent-gateway/internal/pool"
	"agent-gateway/internal/upstream"
)

func TestHeuristic(t *testing.T) {
	tests := []struct {
		prompt string
		want   Intent
	}{
		{"Hello!", Greeting},
		{"hi there", Greeting},
		{"你好", Greeting},
		{"Good morning", Greeting},
		{"thanks a lot", Trivial},
		{"ok", Trivial},
		{"谢谢", Trivial},
		{"what is the capital of france", Complex},
		{"write me a poem about go", Complex},
	}
	for _, tt := range tests {
		if got := Heuristic(tt.prompt); got != tt.want {
			t.Errorf("Heuristic(%q) = %v, want %v", tt.prompt, got, tt.want)
		}
	}
}

func TestQuickReply(t *testing.T) {
	for _, i := range []Intent{Greeting, Trivial} {
		reply := QuickReply(i, "hi")
		if reply == "" {
			t.Errorf("QuickReply(%v) is empty", i)
		}
	}
}

func TestParseReply(t *testing.T) {
	tests := []struct {
		content string
		want    Intent
		wantErr bool
	}{
		{`{"intent": "INTENT_GREETING"}`, Greeting, false},
		{`{"intent":"intent_trivial"}`, Trivial, false},
		{`{"intent": "INTENT_COMPLEX"}`, Complex, false},
		{"not json", 0, true},
		{`{"intent": "INTENT_WHATEVER"}`, 0, true},
		{"{}", 0, true},
	}
	for _, tt := range tests {
		got, err := parseReply(tt.content)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseReply(%q) err = %v, wantErr %v", tt.content, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("parseReply(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}

func TestClassifyUsesLLMAndRecordsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := map[string]any{
			"message":           map[string]string{"role": "assistant", "content": `{"intent":"INTENT_GREETING"}`},
			"prompt_eval_count": 12,
			"eval_count":        4,
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	defer server.Close()

	client := upstream.NewClient(server.URL, time.Second)
	classifier := newTestClassifier(client)

	res, err := classifier.Classify(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent != Greeting {
		t.Errorf("Intent = %v, want Greeting", res.Intent)
	}
	if res.Usage.PromptTokens != 12 || res.Usage.CompletionTokens != 4 {
		t.Errorf("Usage = %+v", res.Usage)
	}
	if classifier.breaker.State() != breaker.StateClosed {
		t.Errorf("breaker state = %v", classifier.breaker.State())
	}
}

func TestClassifyFallsBackWhenUpstreamDown(t *testing.T) {
	client := upstream.NewClient("http://127.0.0.1:1", 100*time.Millisecond)
	classifier := newTestClassifier(client)

	res, err := classifier.Classify(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent != Greeting {
		t.Errorf("Intent = %v, want heuristic Greeting", res.Intent)
	}
	if res.Usage != (upstream.Usage{}) {
		t.Errorf("Usage = %+v, want zero", res.Usage)
	}
}

func TestClassifyFallbackOnUnparseableReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"content": "I am a language model"},
		})
	}))
	defer server.Close()

	classifier := newTestClassifier(upstream.NewClient(server.URL, time.Second))
	res, err := classifier.Classify(context.Background(), "what is the capital of france")
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent != Complex {
		t.Errorf("Intent = %v, want heuristic Complex", res.Intent)
	}
}

func TestClassifyContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
	}))
	defer server.Close()

	classifier := newTestClassifier(upstream.NewClient(server.URL, time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if _, err := classifier.Classify(ctx, "hello"); err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestClassifyQueueFullFallsBack(t *testing.T) {
	blocker := make(chan struct{})
	defer close(blocker)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocker
	}))
	defer server.Close()

	classifier := NewClassifier(
		upstream.NewClient(server.URL, time.Second),
		"qwen2.5:0.5b", 0,
		breaker.New(3, time.Minute),
		pool.New(1, 0), // zero queue: second submit returns ErrQueueFull
	)
	res, err := classifier.Classify(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent != Greeting {
		t.Errorf("Intent = %v, want heuristic fallback", res.Intent)
	}
}

func newTestClassifier(client *upstream.Client) *Classifier {
	return NewClassifier(client, "qwen2.5:0.5b", 1, breaker.New(3, time.Minute), pool.New(4, 16))
}
