package domain

import (
	"errors"
	"math"
	"time"
)

// This service owns one decision, and owns it alone: given an event whose
// delivery failed, should we try again, and when?
//
// The dispatcher deliberately does not answer that. It reports whether a
// delivery worked and leaves the event in RETRYING. Keeping the policy in one
// place is what stops "how many retries do we allow" from being answered
// differently in three services.

var ErrInvalidPolicy = errors.New("invalid retry policy")

// RetryPolicy is exponential backoff with full jitter.
type RetryPolicy struct {
	// MaxAttempts is the total number of asynchronous retries before an event
	// is declared definitively failed.
	MaxAttempts int

	BaseDelay time.Duration
	MaxDelay  time.Duration
}

func NewRetryPolicy(maxAttempts int, base, max time.Duration) (RetryPolicy, error) {
	p := RetryPolicy{MaxAttempts: maxAttempts, BaseDelay: base, MaxDelay: max}
	switch {
	case maxAttempts < 1:
		return p, errors.New("retry policy needs at least one attempt")
	case base <= 0:
		return p, errors.New("retry policy needs a positive base delay")
	case max < base:
		return p, errors.New("retry policy max delay cannot be below the base delay")
	}
	return p, nil
}

// IsExhausted reports whether the budget is spent.
func (p RetryPolicy) IsExhausted(retryCount int) bool {
	return retryCount >= p.MaxAttempts
}

// NextDelay returns how long to wait before retry number retryCount+1.
//
// jitter must be in [0,1). Full jitter — picking uniformly from the whole
// window rather than adding a small wobble to it — is what actually breaks up
// a thundering herd: when a client's endpoint comes back after an outage, the
// hundreds of events waiting on it must not all fire in the same instant, or
// they will knock it over again.
func (p RetryPolicy) NextDelay(retryCount int, jitter float64) time.Duration {
	if retryCount < 0 {
		retryCount = 0
	}
	jitter = clamp01(jitter)

	window := float64(p.BaseDelay) * math.Pow(2, float64(retryCount))
	if window > float64(p.MaxDelay) {
		window = float64(p.MaxDelay)
	}

	// Never return zero: a delay of nothing is a hot loop against an endpoint
	// that is already struggling.
	return min(time.Duration(jitter*window)+p.BaseDelay, p.MaxDelay)
}

func clamp01(v float64) float64 {
	switch {
	case math.IsNaN(v), v < 0:
		return 0
	case v >= 1:
		return math.Nextafter(1, 0)
	default:
		return v
	}
}
