package domain

import "fmt"

// EventState is the global delivery state of a notification event.
//
//	PENDING ──▶ DELIVERING ──┬──▶ DELIVERED   (terminal, success)
//	                         └──▶ RETRYING ──▶ DELIVERING ──▶ ...
//	                                  │
//	                                  └──▶ FAILED  (terminal until replayed)
//
// FAILED is reachable, and reachable on purpose: the case statement asks for a
// notification to be re-sent "when delivery has definitely failed", so there
// has to be a state that means exactly that.
type EventState string

const (
	StatePending    EventState = "PENDING"
	StateDelivering EventState = "DELIVERING"
	StateRetrying   EventState = "RETRYING"
	StateDelivered  EventState = "DELIVERED"
	StateFailed     EventState = "FAILED"
)

func (s EventState) Valid() bool {
	switch s {
	case StatePending, StateDelivering, StateRetrying, StateDelivered, StateFailed:
		return true
	default:
		return false
	}
}

func (s EventState) String() string { return string(s) }

// IsTerminal reports whether the platform will stop acting on the event on its
// own. FAILED is terminal for the platform but not for the client, who may
// still replay it.
func (s EventState) IsTerminal() bool {
	return s == StateDelivered || s == StateFailed
}

func ParseEventState(raw string) (EventState, error) {
	s := EventState(raw)
	if !s.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidEventState, raw)
	}
	return s, nil
}

// DeliveryStatus is the outcome of a single delivery cycle.
type DeliveryStatus string

const (
	DeliverySuccess DeliveryStatus = "SUCCESS"
	DeliveryFailed  DeliveryStatus = "FAILED"
)

func (s DeliveryStatus) Valid() bool {
	return s == DeliverySuccess || s == DeliveryFailed
}

func (s DeliveryStatus) String() string { return string(s) }

// DispatchSource records what triggered a delivery cycle.
//
// This is the value that lets a first delivery, an automatic retry and a manual
// replay share one code path without losing the ability to tell them apart in
// the audit trail.
type DispatchSource string

const (
	SourceSystem       DispatchSource = "SYSTEM"
	SourceRetryService DispatchSource = "RETRY_SERVICE"
	SourceSelfService  DispatchSource = "SELF_SERVICE"
)

func (s DispatchSource) Valid() bool {
	switch s {
	case SourceSystem, SourceRetryService, SourceSelfService:
		return true
	default:
		return false
	}
}

func (s DispatchSource) String() string { return string(s) }

func ParseDispatchSource(raw string) (DispatchSource, error) {
	s := DispatchSource(raw)
	if !s.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidDispatchSource, raw)
	}
	return s, nil
}
