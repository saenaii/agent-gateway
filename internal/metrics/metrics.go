// Package metrics implements a thread-safe in-memory metrics collector with
// a rolling window and percentile calculations.
package metrics

import (
	"sort"
	"sync"
	"time"
)

// Request records one completed gateway request for the live dashboard log.
type Request struct {
	ID         string  `json:"id"`
	Time       string  `json:"time"`
	Intent     string  `json:"intent"`
	Layer      int     `json:"layer"`
	Status     string  `json:"status"`
	TTFT       float64 `json:"ttft_ms"`
	Duration   float64 `json:"duration_ms"`
	PromptTok  int64   `json:"prompt_tokens"`
	Completion int64   `json:"completion_tokens"`
}

// Snapshot is a point-in-time view of the metrics.
type Snapshot struct {
	TotalRequests      int64     `json:"total_requests"`
	Layer1Offload      int64     `json:"layer1_offload"`
	Layer2Escalation   int64     `json:"layer2_escalation"`
	OffloadRatio       float64   `json:"offload_ratio"`
	EscalationRatio    float64   `json:"escalation_ratio"`
	TotalPromptTokens  int64     `json:"total_prompt_tokens"`
	TotalCompletionTok int64     `json:"total_completion_tokens"`
	TotalTokens        int64     `json:"total_tokens"`
	Layer1Tokens       int64     `json:"layer1_tokens"`
	TTFTP50            float64   `json:"ttft_p50_ms"`
	TTFTP90            float64   `json:"ttft_p90_ms"`
	TTFTP99            float64   `json:"ttft_p99_ms"`
	DurationP50        float64   `json:"duration_p50_ms"`
	DurationP90        float64   `json:"duration_p90_ms"`
	DurationP99        float64   `json:"duration_p99_ms"`
	Requests           []Request `json:"requests"`
}

// Collector aggregates counters and rolling samples under a single lock.
type Collector struct {
	mu sync.RWMutex

	totalRequests int64
	layer1Offload int64
	layer2Escal   int64

	totalPrompt   int64
	totalComplete int64
	layer1Tokens  int64

	window time.Duration
	max    int
	logCap int

	ttft      []sample
	durations []sample
	requests  []Request
}

type sample struct {
	at    time.Time
	value float64
}

// New creates a Collector with the given rolling window and sample cap.
func New(window time.Duration, maxSamples, requestLogSize int) *Collector {
	return &Collector{
		window:   window,
		max:      maxSamples,
		logCap:   requestLogSize,
		requests: make([]Request, 0, requestLogSize),
	}
}

// RecordRequest adds one request to the live log, dropping the oldest entry.
func (c *Collector) RecordRequest(r Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, r)
	if len(c.requests) > c.logCap {
		c.requests = append(c.requests[:0], c.requests[1:]...)
	}
}

// RecordTokens adds token usage. local indicates Layer 1 (offline) usage.
func (c *Collector) RecordTokens(prompt, completion int64, local bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalPrompt += prompt
	c.totalComplete += completion
	if local {
		c.layer1Tokens += prompt + completion
	}
}

// RecordRequestResult counts a request by layer and records latency samples.
func (c *Collector) RecordRequestResult(layer int, ttft, duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalRequests++
	if layer == 1 {
		c.layer1Offload++
	} else {
		c.layer2Escal++
	}
	now := time.Now()
	c.ttft = c.appendSample(c.ttft, sample{at: now, value: ms(ttft)})
	c.durations = c.appendSample(c.durations, sample{at: now, value: ms(duration)})
}

func (c *Collector) appendSample(list []sample, s sample) []sample {
	list = append(list, s)
	if n := len(list) - c.max; n > 0 {
		list = append(list[:0], list[n:]...)
	}
	return list
}

// Snapshot returns a consistent view of all metrics.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().Add(-c.window)
	c.ttft = prune(c.ttft, cutoff)
	c.durations = prune(c.durations, cutoff)
	s := Snapshot{
		TotalRequests:      c.totalRequests,
		Layer1Offload:      c.layer1Offload,
		Layer2Escalation:   c.layer2Escal,
		TotalPromptTokens:  c.totalPrompt,
		TotalCompletionTok: c.totalComplete,
		TotalTokens:        c.totalPrompt + c.totalComplete,
		Layer1Tokens:       c.layer1Tokens,
		TTFTP50:            percentile(c.ttft, 50),
		TTFTP90:            percentile(c.ttft, 90),
		TTFTP99:            percentile(c.ttft, 99),
		DurationP50:        percentile(c.durations, 50),
		DurationP90:        percentile(c.durations, 90),
		DurationP99:        percentile(c.durations, 99),
	}
	if s.TotalRequests > 0 {
		s.OffloadRatio = float64(s.Layer1Offload) / float64(s.TotalRequests)
		s.EscalationRatio = float64(s.Layer2Escalation) / float64(s.TotalRequests)
	}
	s.Requests = append([]Request(nil), c.requests...)
	return s
}

func prune(list []sample, cutoff time.Time) []sample {
	for len(list) > 0 && list[0].at.Before(cutoff) {
		list = list[1:]
	}
	return list
}

func percentile(list []sample, p float64) float64 {
	if len(list) == 0 {
		return 0
	}
	values := make([]float64, len(list))
	for i, s := range list {
		values[i] = s.value
	}
	sort.Float64s(values)
	idx := int(float64(len(values)-1) * p / 100)
	return values[idx]
}

func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
