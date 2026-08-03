// Package port declares the whole infrastructure surface of the retry service:
// five outbound ports and nothing else.
package port

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aikssen/notifications-platform/services/retry-service/internal/domain"
)

// RetryStore is the persistence side of the retry loop.
type RetryStore interface {
	// ClaimDue atomically takes ownership of up to limit events that are due
	// for another attempt, increments their retry count, and hides them from
	// other pollers for the visibility window.
	//
	// "Hides them" rather than "locks them": holding a database lock while
	// publishing to Kafka would tie a transaction to the availability of a
	// different system. Instead the claim pushes next_retry_at forward by the
	// visibility timeout, the same way a queue makes a message invisible to
	// other consumers. If this process dies mid-cycle, the window expires and
	// the event simply becomes due again.
	ClaimDue(ctx context.Context, now time.Time, visibility time.Duration, limit int) ([]domain.PendingRetry, error)

	// ScheduleNext records when the claimed event should be tried again.
	ScheduleNext(ctx context.Context, notificationEventID string, at time.Time) error

	// MarkFailed moves an event whose retry budget is spent to FAILED, which
	// is the state a client can replay from.
	MarkFailed(ctx context.Context, notificationEventID, reason string, now time.Time) error

	// OldestRetryingAge reports how long the oldest undelivered event has been
	// waiting. It is the single most useful number for an on-call engineer: a
	// rising value means deliveries are failing faster than they are retried,
	// which a success-rate percentage alone will not show.
	OldestRetryingAge(ctx context.Context) (time.Duration, error)

	// ReclaimStalled rescues events left in DELIVERING by a dispatcher that
	// died mid-delivery. Without it those events are stranded forever: the
	// dispatcher will not touch a DELIVERING event, and the retry poller only
	// looks at RETRYING ones.
	ReclaimStalled(ctx context.Context, olderThan time.Time) (int, error)
}

// DispatchMessage is the wire contract of the delivery topic — the same shape
// the platform's own producers use. Retries re-enter through the identical
// path, differing only in dispatch_source.
type DispatchMessage struct {
	EventID        string          `json:"event_id"`
	ClientID       string          `json:"client_id"`
	EventType      string          `json:"event_type"`
	EventPayload   json.RawMessage `json:"event_payload"`
	DispatchSource string          `json:"dispatch_source"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
}

// EventPublisher puts an event back on the delivery topic.
type EventPublisher interface {
	Publish(ctx context.Context, msg DispatchMessage) error
}

// DeadLetterRecord is what is written when an event's retry budget is spent.
type DeadLetterRecord struct {
	NotificationEventID string          `json:"notification_event_id"`
	EventID             string          `json:"event_id"`
	ClientID            string          `json:"client_id"`
	EventType           string          `json:"event_type"`
	EventPayload        json.RawMessage `json:"event_payload"`
	RetryCount          int             `json:"retry_count"`
	Reason              string          `json:"reason"`
	DeadAt              time.Time       `json:"dead_at"`
}

// DeadLetterPublisher records definitively failed events on the DLQ topic.
//
// The event also stays queryable in PostgreSQL — the topic exists so that
// alerting and any downstream recovery tooling can react to exhaustion as an
// event, without polling the database.
type DeadLetterPublisher interface {
	Publish(ctx context.Context, record DeadLetterRecord) error
}

type Clock interface {
	Now() time.Time
}

// Randomizer supplies the jitter. Injecting it keeps backoff assertions exact
// instead of statistical.
type Randomizer interface {
	Float64() float64
}
