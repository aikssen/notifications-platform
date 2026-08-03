package domain

import (
	"encoding/json"
	"strconv"
	"time"
)

// DispatchSource labels who put an event back on the delivery topic. This
// service always uses RETRY_SERVICE, which is what keeps automatic retries
// distinguishable from a client's manual replay in the audit trail even though
// both travel the identical pipeline.
const DispatchSourceRetry = "RETRY_SERVICE"

// PendingRetry is an event this service has claimed for another delivery
// attempt. It is a narrow read model, not the full notification event: this
// service never needs the delivery history, only enough to re-publish.
type PendingRetry struct {
	ID         string
	EventID    string
	ClientID   string
	EventType  string
	Payload    json.RawMessage
	RetryCount int
	LastError  *string
}

// Outcome is what should happen to a claimed event.
type Outcome int

const (
	// Requeue puts the event back on the delivery topic.
	Requeue Outcome = iota
	// Exhaust declares the event definitively failed. That is the state the
	// case statement's replay requirement refers to: "re-send a notification
	// when delivery has definitely failed". Without this transition, FAILED is
	// unreachable and replay has nothing to act on.
	Exhaust
)

// Decision is the result of applying the policy to one claimed event.
type Decision struct {
	Event   PendingRetry
	Outcome Outcome

	// NextRetryAt is set when the outcome is Requeue.
	NextRetryAt time.Time

	// Reason is set when the outcome is Exhaust.
	Reason string
}

// Decide applies the retry policy to a claimed event.
//
// RetryCount has already been incremented by the claim, so it reflects the
// attempt about to happen.
func Decide(event PendingRetry, policy RetryPolicy, jitter float64, now time.Time) Decision {
	if policy.IsExhausted(event.RetryCount) {
		return Decision{
			Event:   event,
			Outcome: Exhaust,
			Reason:  exhaustionReason(event, policy),
		}
	}

	return Decision{
		Event:       event,
		Outcome:     Requeue,
		NextRetryAt: now.Add(policy.NextDelay(event.RetryCount, jitter)),
	}
}

func exhaustionReason(event PendingRetry, policy RetryPolicy) string {
	reason := "delivery abandoned after " +
		strconv.Itoa(policy.MaxAttempts) + " asynchronous retries"
	if event.LastError != nil && *event.LastError != "" {
		reason += "; last error: " + *event.LastError
	}
	return reason
}
