package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-gateway/internal/api/pb"
	"agent-gateway/internal/breaker"
	"agent-gateway/internal/intent"
	"agent-gateway/internal/metrics"
	"agent-gateway/internal/pool"
	"agent-gateway/internal/upstream"
)

func mockOllama(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
}

func newTestGateway(t *testing.T, ollamaReply string) (*Gateway, *httptest.Server) {
	t.Helper()
	ollama := mockOllama(t, ollamaReply)
	t.Cleanup(ollama.Close)

	collector := metrics.New(time.Hour, 1000, 100)
	classifier := intent.NewClassifier(
		upstream.NewClient(ollama.URL, time.Second),
		"qwen2.5:0.5b", 0,
		breaker.New(3, time.Minute),
		pool.New(4, 16),
	)
	g := &Gateway{Classifier: classifier, Metrics: collector, Dashboard: true, Logger: testLogger()}
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	return g, server
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestChatCompletionsGreeting(t *testing.T) {
	_, server := newTestGateway(t, `{"message":{"content":"{\"intent\":\"INTENT_GREETING\"}"},"prompt_eval_count":12,"eval_count":4}`)

	body := `{"model":"gateway","messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Role    string `json:"role"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int64 `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Choices[0].Message.Content == "" {
		t.Error("greeting reply is empty")
	}
	if reply.Choices[0].Message.Role != "assistant" {
		t.Errorf("role = %q", reply.Choices[0].Message.Role)
	}
	if reply.Usage.PromptTokens != 12 {
		t.Errorf("prompt_tokens = %d, want 12", reply.Usage.PromptTokens)
	}
}

func TestChatCompletionsComplexReturnsEmpty(t *testing.T) {
	g, server := newTestGateway(t, `{"message":{"content":"{\"intent\":\"INTENT_COMPLEX\"}"}}`)

	body := `{"model":"gateway","messages":[{"role":"user","content":"explain quantum physics"}]}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Choices[0].Message.Content != "" {
		t.Errorf("content = %q, want empty (no layer 2)", reply.Choices[0].Message.Content)
	}

	s := g.Metrics.Snapshot()
	if s.Layer2Escalation != 1 {
		t.Errorf("Layer2Escalation = %d, want 1", s.Layer2Escalation)
	}
	if s.OffloadRatio != 0 || s.EscalationRatio != 1 {
		t.Errorf("ratios = %.2f/%.2f", s.OffloadRatio, s.EscalationRatio)
	}
}

func TestChatCompletionsStreaming(t *testing.T) {
	_, server := newTestGateway(t, `{"message":{"content":"{\"intent\":\"INTENT_TRIVIAL\"}"}}`)

	body := `{"model":"gateway","stream":true,"messages":[{"role":"user","content":"ok thanks"}]}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	var chunks []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			chunks = append(chunks, strings.TrimPrefix(line, "data: "))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 3 {
		t.Fatalf("got %d chunks, want >= 3", len(chunks))
	}
	if last := chunks[len(chunks)-1]; last != "[DONE]" {
		t.Errorf("last chunk = %q, want [DONE]", last)
	}
}

func TestChatCompletionsValidation(t *testing.T) {
	_, server := newTestGateway(t, `{}`)

	for _, body := range []string{
		`{"model":"gateway"}`,               // no messages
		`{"model":"gateway","messages":[]}`, // empty messages
		`{"model":"gateway","messages":[{"role":"assistant","content":"hi"}]}`, // last not user
	} {
		resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestMetricsAndRequestsEndpoints(t *testing.T) {
	_, server := newTestGateway(t, `{"message":{"content":"{\"intent\":\"INTENT_GREETING\"}"}}`)

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gateway","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	var snap metrics.Snapshot
	getJSON(t, server.URL+"/metrics", &snap)
	if snap.TotalRequests != 1 || snap.Layer1Offload != 1 {
		t.Errorf("snapshot = %+v", snap)
	}
	if len(snap.Requests) != 1 {
		t.Errorf("requests logged = %d, want 1", len(snap.Requests))
	}
	if snap.Requests[0].Intent != "INTENT_GREETING" {
		t.Errorf("intent = %q", snap.Requests[0].Intent)
	}

	var logResp struct {
		Requests []metrics.Request `json:"requests"`
	}
	getJSON(t, server.URL+"/requests", &logResp)
	if len(logResp.Requests) != 1 {
		t.Errorf("log requests = %d, want 1", len(logResp.Requests))
	}

	var health map[string]string
	getJSON(t, server.URL+"/healthz", &health)
	if health["status"] != "ok" {
		t.Errorf("health = %+v", health)
	}
}

func TestDashboardServed(t *testing.T) {
	_, server := newTestGateway(t, `{}`)
	resp, err := http.Get(server.URL + "/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), "Agent Gateway") {
		t.Error("dashboard page missing title")
	}
}

func TestGRPCChat(t *testing.T) {
	ollama := mockOllama(t, `{"message":{"content":"{\"intent\":\"INTENT_GREETING\"}"}}`)
	defer ollama.Close()

	collector := metrics.New(time.Hour, 1000, 100)
	classifier := intent.NewClassifier(
		upstream.NewClient(ollama.URL, time.Second),
		"qwen2.5:0.5b", 0,
		breaker.New(3, time.Minute),
		pool.New(4, 16),
	)
	srv := NewGRPCServer(classifier, collector)

	reply, err := srv.Chat(context.Background(), &pb.ChatRequest{
		Model: "gateway",
		Messages: []*pb.Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Layer != 1 || reply.Intent != "INTENT_GREETING" {
		t.Errorf("reply = %+v", reply)
	}
	if reply.Content == "" {
		t.Error("greeting content is empty")
	}
	if collector.Snapshot().TotalRequests != 1 {
		t.Errorf("TotalRequests = %d, want 1", collector.Snapshot().TotalRequests)
	}
}

func TestGRPCChatNoMessages(t *testing.T) {
	srv := NewGRPCServer(nil, metrics.New(time.Hour, 1000, 100))
	if _, err := srv.Chat(context.Background(), &pb.ChatRequest{}); err == nil {
		t.Fatal("expected error for empty messages")
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: status = %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}
