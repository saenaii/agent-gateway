// Package api exposes the gateway's HTTP and gRPC endpoints.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"agent-gateway/internal/dashboard"
	"agent-gateway/internal/intent"
	"agent-gateway/internal/layer2"
	"agent-gateway/internal/metrics"
	"agent-gateway/internal/upstream"
)

// Gateway composes the runtime dependencies of the HTTP API.
type Gateway struct {
	Classifier      *intent.Classifier
	Layer2          *layer2.Router
	Metrics         *metrics.Collector
	Dashboard       bool
	ClassifyTimeout time.Duration
	Logger          *slog.Logger
}

type chatCompletionRequest struct {
	Model    string             `json:"model"`
	Messages []upstream.Message `json:"messages"`
	Stream   bool               `json:"stream"`
}

type chatCompletionError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Handler returns the HTTP mux with all gateway routes.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.handleChatCompletions)
	mux.HandleFunc("GET /metrics", g.handleMetrics)
	mux.HandleFunc("GET /requests", g.handleRequests)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	if g.Dashboard {
		mux.Handle("GET /dashboard", http.RedirectHandler("/dashboard/", http.StatusMovedPermanently))
		mux.Handle("GET /dashboard/", http.StripPrefix("/dashboard/", dashboard.Handler()))
	}
	return mux
}

func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req chatCompletionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed request body", err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "", "messages is required")
		return
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" || strings.TrimSpace(last.Content) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "", "last message must be from the user")
		return
	}

	start := time.Now()
	classifyCtx, cancel := context.WithTimeout(r.Context(), g.classifyTimeout())
	defer cancel()

	result, err := g.Classifier.Classify(classifyCtx, last.Content)
	if err != nil {
		writeError(w, http.StatusRequestTimeout, "classifier_timeout", "", "intent classification failed")
		return
	}
	if result.Usage.PromptTokens > 0 || result.Usage.CompletionTokens > 0 {
		g.Metrics.RecordTokens(result.Usage.PromptTokens, result.Usage.CompletionTokens, true)
	}

	layer := result.Intent.Layer()
	content := ""
	usage := result.Usage
	if layer == 1 {
		content = intent.QuickReply(result.Intent, last.Content)
	} else if g.Layer2 != nil {
		resp, err := g.Layer2.Chat(r.Context(), req.Model, layer2.ChatRequest{Messages: req.Messages})
		if err != nil {
			g.Logger.Warn("layer2 chat failed", "err", err)
			writeError(w, http.StatusBadGateway, "upstream_error", "layer2_error", err.Error())
			return
		}
		content = resp.Content
		usage = resp.Usage
		if usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
			g.Metrics.RecordTokens(usage.PromptTokens, usage.CompletionTokens, false)
		}
	}

	id := newID()
	created := time.Now().Unix()
	if req.Stream {
		g.streamResponse(w, req, id, created, layer, result.Intent, content, start)
		return
	}

	ttft := time.Since(start)
	g.recordRequest(id, result.Intent, layer, "ok", ttft, time.Since(start))
	writeJSON(w, http.StatusOK, g.buildResponse(req.Model, id, created, content, usage))
}

// classifyTimeout is the budget for a full classify attempt, defaulting to 10s.
func (g *Gateway) classifyTimeout() time.Duration {
	if g.ClassifyTimeout > 0 {
		return g.ClassifyTimeout
	}
	return 10 * time.Second
}

func (g *Gateway) streamResponse(w http.ResponseWriter, req chatCompletionRequest, id string, created int64, layer int, i intent.Intent, content string, start time.Time) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "", "streaming not supported")
		return
	}

	writeSSE := func(evt string) error {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", evt); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	deltaChunk := func(chunk, role, finish string) string {
		b, _ := json.Marshal(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   req.Model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]string{"role": role, "content": chunk},
				"finish_reason": finish,
			}},
		})
		return string(b)
	}

	if err := writeSSE(deltaChunk("", "assistant", "")); err != nil {
		g.abortStream(id, i, layer, start, err)
		return
	}
	if content != "" {
		if err := writeSSE(deltaChunk(content, "", "")); err != nil {
			g.abortStream(id, i, layer, start, err)
			return
		}
	}
	ttft := time.Since(start)
	if err := writeSSE(deltaChunk("", "", "stop")); err != nil {
		g.abortStream(id, i, layer, start, err)
		return
	}
	if err := writeSSE("[DONE]"); err != nil {
		g.abortStream(id, i, layer, start, err)
		return
	}
	g.recordRequest(id, i, layer, "ok", ttft, time.Since(start))
}

func (g *Gateway) abortStream(id string, i intent.Intent, layer int, start time.Time, err error) {
	g.Logger.Warn("stream write failed", "id", id, "err", err)
	g.recordRequest(id, i, layer, "error", time.Since(start), time.Since(start))
}

func (g *Gateway) buildResponse(model, id string, created int64, content string, usage upstream.Usage) map[string]any {
	return map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]int64{
			"prompt_tokens":     usage.PromptTokens,
			"completion_tokens": usage.CompletionTokens,
			"total_tokens":      usage.PromptTokens + usage.CompletionTokens,
		},
	}
}

func (g *Gateway) recordRequest(id string, i intent.Intent, layer int, status string, ttft, duration time.Duration) {
	g.Metrics.RecordRequestResult(layer, ttft, duration)
	g.Metrics.RecordRequest(metrics.Request{
		ID:       id,
		Time:     time.Now().Format(time.RFC3339),
		Intent:   i.String(),
		Layer:    layer,
		Status:   status,
		TTFT:     float64(ttft) / float64(time.Millisecond),
		Duration: float64(duration) / float64(time.Millisecond),
	})
}

func (g *Gateway) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, g.Metrics.Snapshot())
}

func (g *Gateway) handleRequests(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"requests": g.Metrics.Snapshot().Requests})
}

func writeError(w http.ResponseWriter, status int, code, typ, message string) {
	var e chatCompletionError
	e.Error.Message = message
	e.Error.Type = typ
	e.Error.Code = code
	if typ == "" {
		e.Error.Type = code
	}
	if message == "" {
		e.Error.Message = code
	}
	writeJSON(w, status, e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return "chatcmpl-" + hex.EncodeToString(b[:])
}
