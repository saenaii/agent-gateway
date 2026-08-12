package breaker

import (
	"testing"
	"time"
)

func TestTripsAndRecovers(t *testing.T) {
	b := New(3, 10*time.Millisecond)
	for range 3 {
		b.Failure()
	}
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open", b.State())
	}
	if b.Allow() {
		t.Fatal("Allow() = true while open")
	}
	time.Sleep(15 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("Allow() = false after cooldown, want half-open probe")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("state = %v, want half-open", b.State())
	}
	b.Success()
	if b.State() != StateClosed {
		t.Fatalf("state = %v, want closed after probe success", b.State())
	}
	if !b.Allow() {
		t.Fatal("Allow() = false when closed")
	}
}

func TestSuccessResetsFailures(t *testing.T) {
	b := New(3, time.Minute)
	b.Failure()
	b.Failure()
	b.Success()
	b.Failure()
	b.Failure()
	if b.State() != StateClosed {
		t.Fatal("success should have reset the failure count")
	}
	b.Failure()
	if b.State() != StateOpen {
		t.Fatal("state should be open once failures reach the threshold again")
	}
}

func TestHalfOpenAllowsOneProbe(t *testing.T) {
	b := New(1, time.Millisecond)
	b.Failure()
	time.Sleep(5 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("first probe denied")
	}
	if b.Allow() {
		t.Fatal("second probe allowed within cooldown")
	}
	time.Sleep(5 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("probe after cooldown denied")
	}
}

func TestHalfOpenFailureTripsAgain(t *testing.T) {
	b := New(1, time.Millisecond)
	b.Failure()
	time.Sleep(5 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("probe denied")
	}
	b.Failure()
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open after failed probe", b.State())
	}
}
