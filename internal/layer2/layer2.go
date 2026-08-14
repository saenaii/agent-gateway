// Package layer2 implements the escalation path for complex intents: a
// provider registry that routes prompts to configured online LLM backends
// behind a per-provider circuit breaker and retry loop. OpenCode is the
// first provider; OpenAI, Bedrock, and others implement the same Provider
// interface.
package layer2

import (
	"context"
	"fmt"
	"sync"
	"time"

	"agent-gateway/internal/breaker"
	"agent-gateway/internal/upstream"
)

// ChatRequest is the input to a Layer 2 provider.
type ChatRequest struct {
	Messages []upstream.Message
}

// ChatResponse is the reply from a Layer 2 provider.
type ChatResponse struct {
	Content string
	Usage   upstream.Usage
}

// Provider is an online LLM backend. Every supported backend implements
// Chat, returning the assistant text and token usage.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// Router selects the provider for an escalated prompt and guards each
// provider with its own circuit breaker and retry loop.
type Router struct {
	mu        sync.RWMutex
	def       string
	retries   int
	threshold int
	cooldown  time.Duration
	entries   map[string]*entry
}

type entry struct {
	name     string
	provider Provider
	breaker  *breaker.Breaker
}

// NewRouter builds a Router that falls back to def when the requested
// provider name is unknown. retries is the number of retries after the first
// attempt; threshold and cooldown tune the per-provider circuit breaker.
func NewRouter(def string, retries, threshold int, cooldown time.Duration) *Router {
	return &Router{
		def:       def,
		retries:   retries,
		threshold: threshold,
		cooldown:  cooldown,
		entries:   make(map[string]*entry),
	}
}

// Add registers a provider under name.
func (r *Router) Add(name string, p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[name] = &entry{
		name:     name,
		provider: p,
		breaker:  breaker.New(r.threshold, r.cooldown),
	}
}

// Chat escalates the conversation to the named provider, or to the default
// provider when name is empty or unknown. A tripped breaker or persistent
// upstream failure returns an error.
func (r *Router) Chat(ctx context.Context, provider string, req ChatRequest) (*ChatResponse, error) {
	ent, err := r.entry(provider)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt <= r.retries; attempt++ {
		if !ent.breaker.Allow() {
			return nil, fmt.Errorf("layer2 provider %q: circuit breaker open", ent.name)
		}
		resp, err := ent.provider.Chat(ctx, req)
		if err == nil {
			ent.breaker.Success()
			return resp, nil
		}
		lastErr = err
		ent.breaker.Failure()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("layer2 provider %q: %w", ent.name, lastErr)
}

func (r *Router) entry(name string) (*entry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name != "" {
		if ent, ok := r.entries[name]; ok {
			return ent, nil
		}
	}
	ent, ok := r.entries[r.def]
	if !ok {
		return nil, fmt.Errorf("layer2: no provider registered (default %q)", r.def)
	}
	return ent, nil
}
