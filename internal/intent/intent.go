// Package intent implements the Layer 1 intent classifier: a local LLM call
// backed by a keyword heuristic fallback, plus instant quick replies for
// non-complex intents.
package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"agent-gateway/internal/breaker"
	"agent-gateway/internal/pool"
	"agent-gateway/internal/upstream"
)

// Intent is the Layer 1 classification result.
type Intent int

// Intent categories.
const (
	Greeting Intent = iota + 1
	Trivial
	Complex
)

func (i Intent) String() string {
	switch i {
	case Greeting:
		return "INTENT_GREETING"
	case Trivial:
		return "INTENT_TRIVIAL"
	case Complex:
		return "INTENT_COMPLEX"
	default:
		return "INTENT_UNKNOWN"
	}
}

// Layer returns the gateway layer handling this intent (1 or 2).
func (i Intent) Layer() int {
	if i == Complex {
		return 2
	}
	return 1
}

const classifyPrompt = `You are a query classifier for an LLM gateway.
Classify the user's message into exactly one category:
- INTENT_GREETING: greetings, salutations, hellos
- INTENT_TRIVIAL: thanks, acknowledgments, and short casual messages needing no reasoning
- INTENT_COMPLEX: anything requiring knowledge, reasoning, computation, or tools
Respond ONLY with a JSON object like: {"intent": "INTENT_GREETING"}`

type classifyReply struct {
	Intent string `json:"intent"`
}

// Classifier runs Layer 1 classification with retry, a circuit breaker, and a
// worker pool, falling back to a heuristic when the local LLM is unavailable.
type Classifier struct {
	client  *upstream.Client
	model   string
	retries int
	breaker *breaker.Breaker
	pool    *pool.Pool
}

// NewClassifier wires the classifier to an Ollama client.
func NewClassifier(client *upstream.Client, model string, retries int, br *breaker.Breaker, p *pool.Pool) *Classifier {
	return &Classifier{
		client:  client,
		model:   model,
		retries: retries,
		breaker: br,
		pool:    p,
	}
}

// Result carries the classification outcome.
type Result struct {
	Intent Intent
	Usage  upstream.Usage
}

// Classify determines the intent of prompt. It never fails for transport
// reasons: any Layer 1 failure degrades to the heuristic. The only returned
// error is context cancellation.
func (c *Classifier) Classify(ctx context.Context, prompt string) (Result, error) {
	res := make(chan Result, 1)
	err := c.pool.Submit(ctx, func(ctx context.Context) error {
		res <- c.classifyLLM(ctx, prompt)
		return nil
	})
	if err == pool.ErrQueueFull {
		return Result{Intent: Heuristic(prompt)}, nil
	}
	select {
	case r := <-res:
		return r, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (c *Classifier) classifyLLM(ctx context.Context, prompt string) Result {
	if !c.breaker.Allow() {
		slog.Debug("classifier breaker open, using heuristic", "model", c.model)
		return Result{Intent: Heuristic(prompt)}
	}
	for attempt := 0; attempt <= c.retries; attempt++ {
		resp, err := c.client.Chat(ctx, upstream.ChatRequest{
			Model: c.model,
			Messages: []upstream.Message{
				{Role: "system", Content: classifyPrompt},
				{Role: "user", Content: prompt},
			},
			Format:  "json",
			Options: upstream.Options{Temperature: 0},
		})
		if err == nil {
			c.breaker.Success()
			parsed, err := parseReply(resp.Content)
			if err == nil {
				return Result{Intent: parsed, Usage: resp.Usage}
			}
			slog.Debug("classifier returned unparseable reply", "attempt", attempt, "err", err)
		} else {
			slog.Debug("classifier upstream call failed", "attempt", attempt, "err", err)
		}
		c.breaker.Failure()
		if ctx.Err() != nil {
			break
		}
	}
	return Result{Intent: Heuristic(prompt)}
}

func parseReply(content string) (Intent, error) {
	var reply classifyReply
	if err := json.Unmarshal([]byte(content), &reply); err != nil {
		return 0, fmt.Errorf("parse classifier json: %w", err)
	}
	switch strings.ToUpper(strings.TrimSpace(reply.Intent)) {
	case "INTENT_GREETING":
		return Greeting, nil
	case "INTENT_TRIVIAL":
		return Trivial, nil
	case "INTENT_COMPLEX":
		return Complex, nil
	default:
		return 0, fmt.Errorf("unknown intent %q", reply.Intent)
	}
}

// Heuristic classifies a prompt with keyword rules; it is the fallback when
// the local LLM is unreachable.
func Heuristic(prompt string) Intent {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if containsAny(lower, greetingWords...) {
		return Greeting
	}
	if containsAny(lower, trivialWords...) {
		return Trivial
	}
	return Complex
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

var greetingWords = []string{
	"hello", "hi", "hey", "howdy", "yo", "greetings",
	"good morning", "good afternoon", "good evening",
	"你好", "嗨", "早上好", "下午好", "晚上好",
}

var trivialWords = []string{
	"thanks", "thank you", "thx", "ty", "ok", "okay", "sure", "k",
	"bye", "goodbye", "see you", "yes", "no", "thanks a lot",
	"谢谢", "好的", "再见", "行", "可以",
}

var greetingReplies = []string{
	"Hello! How can I help you today?",
	"Hi there! What can I do for you?",
	"Hey! How can I assist you?",
}

var trivialReplies = []string{
	"Got it.",
	"Okay, noted.",
	"Sure thing!",
}

// QuickReply returns an instant, zero-token-cost response for greeting and
// trivial intents.
func QuickReply(i Intent, prompt string) string {
	variants := greetingReplies
	if i == Trivial {
		variants = trivialReplies
	}
	return variants[len(prompt)%len(variants)]
}
