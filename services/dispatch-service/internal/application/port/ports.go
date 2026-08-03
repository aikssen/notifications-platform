// Package port declares every edge of the dispatch service.
//
// This file is the entire infrastructure surface of the service: one inbound
// port and six outbound ports. Everything the use cases can reach is here, and
// nothing here mentions Kafka, PostgreSQL or HTTP.
//
// The linter enforces that (see .golangci.yml): importing an infrastructure
// package from application/ or domain/ fails the build.
package port

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

// ---------------------------------------------------------------------------
// Inbound (driving) port — what the outside world asks this service to do
// ---------------------------------------------------------------------------

// IncomingEvent is a delivery request as it arrives from the platform.
type IncomingEvent struct {
	EventID        string
	ClientID       string
	EventType      string
	Payload        json.RawMessage
	DispatchSource domain.DispatchSource
	CorrelationID  string
}

// EventProcessor handles one incoming delivery request. The Kafka consumer
// adapter depends on this interface, not on a concrete use case.
type EventProcessor interface {
	Process(ctx context.Context, in IncomingEvent) error
}

// ---------------------------------------------------------------------------
// Outbound (driven) ports — what this service needs from the outside world
// ---------------------------------------------------------------------------

// EventStore persists notification events and their delivery attempts.
//
// RecordOutcome is a single method on purpose: writing the attempt and moving
// the event state are one business fact, and splitting them across two calls
// is what lets an audit trail drift from the state it is supposed to explain.
type EventStore interface {
	// FindByEventID looks an event up by its upstream identifier.
	// Returns (nil, nil) when it does not exist.
	FindByEventID(ctx context.Context, eventID string) (*domain.NotificationEvent, error)

	// Insert stores a newly ingested event. It is idempotent: if another
	// instance already inserted the same EventID, it reports inserted=false
	// instead of failing, and the caller re-reads the winning row.
	Insert(ctx context.Context, event *domain.NotificationEvent) (inserted bool, err error)

	// ClaimForDelivery moves the event to DELIVERING only if it is still in
	// the state the caller observed. It returns false when another instance
	// claimed it first — this is what keeps at-least-once delivery from
	// turning into concurrent duplicate delivery.
	ClaimForDelivery(ctx context.Context, eventID string, from domain.EventState) (claimed bool, err error)

	// RecordOutcome appends the attempt and updates the event state in one
	// transaction, assigning the attempt number atomically.
	RecordOutcome(ctx context.Context, event *domain.NotificationEvent, attempt *domain.NotificationAttempt) error

	// UpdateState persists a state change that produced no delivery attempt,
	// such as an event whose client has no active subscription.
	UpdateState(ctx context.Context, event *domain.NotificationEvent) error
}

// SubscriptionResolver answers the mandatory question from the case statement:
// is this event actually deliverable, and to whom?
//
// Returns (nil, nil) when the client has no active subscription for the event
// type — a normal outcome, not an error.
type SubscriptionResolver interface {
	Resolve(ctx context.Context, clientID, eventType string) (*domain.Subscription, error)
}

// WebhookRequest is one outbound call to a client endpoint.
type WebhookRequest struct {
	Subscription domain.Subscription
	Payload      json.RawMessage
	Headers      map[string]string
}

// WebhookResponse is what came back, including the failed cases: a transport
// error is reported through the error return, an HTTP error through Status.
type WebhookResponse struct {
	Status   int
	Body     json.RawMessage
	Duration time.Duration
	Attempts int // in-process attempts spent, for logs and metrics only
}

// WebhookSender delivers the payload to a client endpoint. Immediate retries
// of transient failures happen inside the adapter and are not persisted.
type WebhookSender interface {
	Send(ctx context.Context, req WebhookRequest) (WebhookResponse, error)
}

// DeliveryResult is the fact published after every delivery cycle.
type DeliveryResult struct {
	NotificationEventID string                `json:"notification_event_id"`
	EventID             string                `json:"event_id"`
	ClientID            string                `json:"client_id"`
	EventType           string                `json:"event_type"`
	State               domain.EventState     `json:"state"`
	Status              domain.DeliveryStatus `json:"status"`
	DispatchSource      domain.DispatchSource `json:"dispatch_source"`
	AttemptNumber       int                   `json:"attempt_number"`
	WebhookURL          string                `json:"webhook_url"`
	ResponseStatus      *int                  `json:"response_status,omitempty"`
	ErrorMessage        *string               `json:"error_message,omitempty"`
	DurationMS          int                   `json:"duration_ms"`
	CorrelationID       string                `json:"correlation_id,omitempty"`
	OccurredAt          time.Time             `json:"occurred_at"`
}

// ResultPublisher emits delivery outcomes as events.
//
// This is what keeps observability out of the delivery path: the monitoring
// stack subscribes to this stream, and can be replaced or removed without
// touching a line of dispatch logic.
type ResultPublisher interface {
	Publish(ctx context.Context, result DeliveryResult) error
}

// Clock exists so time-dependent behaviour is testable without sleeping.
type Clock interface {
	Now() time.Time
}

// IDGenerator produces the identifiers this service owns.
type IDGenerator interface {
	NewID() string
}
