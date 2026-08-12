package metrics

import (
	"testing"
	"time"
)

func TestRecordRequestResultRatios(t *testing.T) {
	c := New(time.Hour, 100, 10)
	for range 3 {
		c.RecordRequestResult(1, 10*time.Millisecond, 20*time.Millisecond)
	}
	c.RecordRequestResult(2, 30*time.Millisecond, 40*time.Millisecond)

	s := c.Snapshot()
	if s.TotalRequests != 4 {
		t.Errorf("TotalRequests = %d, want 4", s.TotalRequests)
	}
	if s.Layer1Offload != 3 || s.Layer2Escalation != 1 {
		t.Errorf("layers = %d/%d, want 3/1", s.Layer1Offload, s.Layer2Escalation)
	}
	if s.OffloadRatio != 0.75 || s.EscalationRatio != 0.25 {
		t.Errorf("ratios = %.2f/%.2f, want 0.75/0.25", s.OffloadRatio, s.EscalationRatio)
	}
}

func TestPercentiles(t *testing.T) {
	c := New(time.Hour, 100, 10)
	for i := range 100 {
		c.RecordRequestResult(1, time.Duration(i)*time.Millisecond, time.Duration(i)*time.Millisecond)
	}
	s := c.Snapshot()
	if s.DurationP50 != 49 {
		t.Errorf("P50 = %v, want 49", s.DurationP50)
	}
	if s.DurationP99 != 98 {
		t.Errorf("P99 = %v, want 98", s.DurationP99)
	}
	if s.DurationP90 != 89 {
		t.Errorf("P90 = %v, want 89", s.DurationP90)
	}
}

func TestPercentileEmpty(t *testing.T) {
	c := New(time.Hour, 100, 10)
	s := c.Snapshot()
	if s.DurationP99 != 0 || s.TTFTP50 != 0 {
		t.Errorf("empty snapshot percentiles should be 0, got %v/%v", s.DurationP99, s.TTFTP50)
	}
}

func TestTokenAccounting(t *testing.T) {
	c := New(time.Hour, 100, 10)
	c.RecordTokens(10, 20, true)
	c.RecordTokens(5, 5, false)
	s := c.Snapshot()
	if s.TotalTokens != 40 {
		t.Errorf("TotalTokens = %d, want 40", s.TotalTokens)
	}
	if s.Layer1Tokens != 30 {
		t.Errorf("Layer1Tokens = %d, want 30", s.Layer1Tokens)
	}
}

func TestRollingWindowPrunesExpired(t *testing.T) {
	c := New(time.Millisecond, 100, 10)
	c.RecordRequestResult(1, 10*time.Millisecond, 10*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	s := c.Snapshot()
	if s.TotalRequests != 1 {
		t.Fatalf("TotalRequests = %d, want 1 (counters are cumulative)", s.TotalRequests)
	}
	if s.DurationP50 != 0 {
		t.Errorf("P50 = %v, want 0 after window expiry", s.DurationP50)
	}
}

func TestRequestLogBounded(t *testing.T) {
	c := New(time.Hour, 100, 3)
	for i := range 5 {
		c.RecordRequest(Request{ID: string(rune('a' + i))})
	}
	if got := len(c.Snapshot().Requests); got != 3 {
		t.Errorf("request log length = %d, want 3", got)
	}
}

func TestSnapshotDoesNotAlias(t *testing.T) {
	c := New(time.Hour, 100, 3)
	c.RecordRequest(Request{ID: "x"})
	s := c.Snapshot()
	s.Requests[0].ID = "mutated"
	if got := c.Snapshot().Requests[0].ID; got != "x" {
		t.Errorf("request log mutated: %q", got)
	}
}
