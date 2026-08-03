package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// allowedTransitions is the single source of truth for the event lifecycle.
//
// Everything else in this service is plumbing around this table: the Kafka
// consumer decides *when* to attempt a delivery, the HTTP adapter decides
// *how*, but only this map decides what an event is allowed to become.
var allowedTransitions = map[EventState][]EventState{
	StatePending:    {StateDelivering},
	StateDelivering: {StateDelivered, StateRetrying, StateFailed},
	StateRetrying:   {StateDelivering},
	StateDelivered:  {},              // terminal: a delivered event is never re-delivered
	StateFailed:     {StateRetrying}, // a client replay re-opens a failed event
}

// NotificationEvent is a business event that must be delivered to a client
// webhook, together with its global delivery state.
//
// It owns the original payload, so a replay months later sends exactly what
// the platform originally produced — not a reconstruction of it.
type NotificationEvent struct {
	// ID is ours: the notification_event_id clients see in the API.
	ID string

	// EventID comes from the upstream platform and is the idempotency key.
	// Its format is not ours to choose, hence a plain string.
	EventID   string
	ClientID  string
	EventType string
	Payload   json.RawMessage

	state      EventState
	retryCount int
	lastError  *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewNotificationEvent creates an event freshly ingested from the platform.
func NewNotificationEvent(
	id, eventID, clientID, eventType string,
	payload json.RawMessage,
	now time.Time,
) (*NotificationEvent, error) {
	switch {
	case eventID == "":
		return nil, ErrMissingEventID
	case clientID == "":
		return nil, ErrMissingClientID
	case eventType == "":
		return nil, ErrMissingEventType
	case len(payload) == 0:
		return nil, ErrMissingPayload
	}

	return &NotificationEvent{
		ID:        id,
		EventID:   eventID,
		ClientID:  clientID,
		EventType: eventType,
		Payload:   payload,
		state:     StatePending,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// RehydrateNotificationEvent rebuilds an event from storage. It deliberately
// skips the transition rules: persisted state is history, not a new decision.
func RehydrateNotificationEvent(
	id, eventID, clientID, eventType string,
	payload json.RawMessage,
	state EventState,
	retryCount int,
	lastError *string,
	createdAt, updatedAt time.Time,
) *NotificationEvent {
	return &NotificationEvent{
		ID:         id,
		EventID:    eventID,
		ClientID:   clientID,
		EventType:  eventType,
		Payload:    payload,
		state:      state,
		retryCount: retryCount,
		lastError:  lastError,
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
	}
}

func (e *NotificationEvent) State() EventState { return e.state }
func (e *NotificationEvent) RetryCount() int   { return e.retryCount }
func (e *NotificationEvent) LastError() *string {
	return e.lastError
}

// CanBeDispatched reports whether a delivery attempt may start now.
func (e *NotificationEvent) CanBeDispatched() bool {
	return e.state == StatePending || e.state == StateRetrying
}

// BeginDelivery moves the event into DELIVERING, claiming it for an attempt.
func (e *NotificationEvent) BeginDelivery(now time.Time) error {
	return e.transitionTo(StateDelivering, now)
}

// MarkDelivered records a successful delivery. Terminal.
func (e *NotificationEvent) MarkDelivered(now time.Time) error {
	if err := e.transitionTo(StateDelivered, now); err != nil {
		return err
	}
	e.lastError = nil
	return nil
}

// MarkRetrying records a failed delivery cycle that is still worth retrying.
//
// Note what this method does NOT do: it does not decide whether the retry
// budget is exhausted, and it does not schedule the next attempt. Deciding
// "should we try again, and when?" belongs to the retry service, which owns
// the retry policy. This service only answers "did it work?".
func (e *NotificationEvent) MarkRetrying(reason string, now time.Time) error {
	if err := e.transitionTo(StateRetrying, now); err != nil {
		return err
	}
	e.lastError = &reason
	return nil
}

// MarkFailed closes the event after the retry budget is spent. The event stays
// replayable by the client through the self-service API.
func (e *NotificationEvent) MarkFailed(reason string, now time.Time) error {
	if err := e.transitionTo(StateFailed, now); err != nil {
		return err
	}
	e.lastError = &reason
	return nil
}

func (e *NotificationEvent) transitionTo(next EventState, now time.Time) error {
	if !next.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidEventState, next)
	}
	if !slices.Contains(allowedTransitions[e.state], next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, e.state, next)
	}
	e.state = next
	e.UpdatedAt = now
	return nil
}
