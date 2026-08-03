package domain_test

import (
	"testing"
	"time"

	"github.com/aikssen/notifications-platform/services/retry-service/internal/domain"
)

func policy(t *testing.T) domain.RetryPolicy {
	t.Helper()
	p, err := domain.NewRetryPolicy(5, 10*time.Second, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBackoffGrowsExponentiallyAndIsCapped(t *testing.T) {
	p := policy(t)

	// With the jitter at its maximum, the delay tracks the top of the window,
	// which is what makes the growth observable.
	const full = 0.999999

	var previous time.Duration
	for retryCount := range 4 {
		got := p.NextDelay(retryCount, full)
		if got <= previous {
			t.Fatalf("retry %d waits %v, which is not longer than the previous %v",
				retryCount, got, previous)
		}
		previous = got
	}

	if got := p.NextDelay(50, full); got > p.MaxDelay {
		t.Fatalf("delay %v exceeded the cap %v", got, p.MaxDelay)
	}
}

// Full jitter is the point: two events failing at the same instant must not
// come back at the same instant.
func TestJitterSpreadsTheWindow(t *testing.T) {
	p := policy(t)

	low := p.NextDelay(3, 0)
	high := p.NextDelay(3, 0.999999)

	if low >= high {
		t.Fatalf("jitter has no effect: low=%v high=%v", low, high)
	}
	if low < p.BaseDelay {
		t.Fatalf("delay %v is below the base delay — a retry must never be immediate", low)
	}
}

func TestNextDelayIsRobustToBadInput(t *testing.T) {
	p := policy(t)

	for _, jitter := range []float64{-1, 0, 1, 2} {
		d := p.NextDelay(0, jitter)
		if d < p.BaseDelay || d > p.MaxDelay {
			t.Fatalf("jitter %v produced %v, outside [%v, %v]", jitter, d, p.BaseDelay, p.MaxDelay)
		}
	}

	if d := p.NextDelay(-5, 0.5); d < p.BaseDelay {
		t.Fatalf("a negative retry count produced %v", d)
	}
}

func TestExhaustionBoundary(t *testing.T) {
	p := policy(t) // MaxAttempts = 5

	for count, want := range map[int]bool{0: false, 4: false, 5: true, 9: true} {
		if got := p.IsExhausted(count); got != want {
			t.Errorf("IsExhausted(%d) = %v, want %v", count, got, want)
		}
	}
}

func TestPolicyValidation(t *testing.T) {
	tests := []struct {
		name        string
		maxAttempts int
		base, max   time.Duration
	}{
		{"no attempts", 0, time.Second, time.Minute},
		{"zero base delay", 3, 0, time.Minute},
		{"cap below base", 3, time.Minute, time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := domain.NewRetryPolicy(tc.maxAttempts, tc.base, tc.max); err == nil {
				t.Fatal("expected the policy to be rejected")
			}
		})
	}
}

// The transition that makes the case statement's replay requirement possible.
func TestDecideExhaustsWhenBudgetIsSpent(t *testing.T) {
	p := policy(t)
	now := time.Date(2026, 3, 15, 9, 30, 22, 0, time.UTC)
	lastError := "connection refused"

	spent := domain.Decide(
		domain.PendingRetry{ID: "e1", RetryCount: 5, LastError: &lastError}, p, 0.5, now)
	if spent.Outcome != domain.Exhaust {
		t.Fatal("a spent budget must exhaust the event so it becomes replayable")
	}
	if spent.Reason == "" {
		t.Fatal("the reason must be recorded for whoever investigates later")
	}

	remaining := domain.Decide(domain.PendingRetry{ID: "e2", RetryCount: 2}, p, 0.5, now)
	if remaining.Outcome != domain.Requeue {
		t.Fatal("an event with budget left must be requeued")
	}
	if !remaining.NextRetryAt.After(now) {
		t.Fatalf("next retry at %v is not in the future", remaining.NextRetryAt)
	}
}
