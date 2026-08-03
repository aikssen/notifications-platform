package domain

import "errors"

var (
	// ErrInvalidTransition is returned when a caller tries to move an event
	// between two states the lifecycle does not connect.
	ErrInvalidTransition = errors.New("invalid state transition")

	ErrInvalidEventState     = errors.New("invalid event state")
	ErrInvalidDispatchSource = errors.New("invalid dispatch source")

	// ErrNotDispatchable means the event exists but its current state says it
	// must not be delivered right now (already delivered, or in flight).
	ErrNotDispatchable = errors.New("event is not in a dispatchable state")

	ErrMissingEventID   = errors.New("event_id is required")
	ErrMissingClientID  = errors.New("client_id is required")
	ErrMissingEventType = errors.New("event_type is required")
	ErrMissingPayload   = errors.New("event_payload is required")
)
