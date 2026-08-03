package domain_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

var now = time.Date(2026, 3, 15, 9, 30, 22, 0, time.UTC)

func newEvent(t *testing.T, state domain.EventState) *domain.NotificationEvent {
	t.Helper()
	return domain.RehydrateNotificationEvent(
		"11111111-1111-4111-8111-111111111111",
		"EVT001",
		"CLIENT001",
		"credit_card_payment",
		json.RawMessage(`{"content":"Credit card payment received for $150.00"}`),
		state, 0, nil, now, now,
	)
}

// The lifecycle is the one invariant this service defends. Every legal and
// illegal edge is asserted explicitly, so a future refactor cannot quietly
// widen it.
func TestLifecycleTransitions(t *testing.T) {
	type step struct {
		name string
		do   func(*domain.NotificationEvent) error
	}

	begin := step{"BeginDelivery", func(e *domain.NotificationEvent) error { return e.BeginDelivery(now) }}
	deliver := step{"MarkDelivered", func(e *domain.NotificationEvent) error { return e.MarkDelivered(now) }}
	retry := step{"MarkRetrying", func(e *domain.NotificationEvent) error { return e.MarkRetrying("boom", now) }}
	fail := step{"MarkFailed", func(e *domain.NotificationEvent) error { return e.MarkFailed("budget spent", now) }}

	tests := []struct {
		from    domain.EventState
		step    step
		want    domain.EventState
		allowed bool
	}{
		{domain.StatePending, begin, domain.StateDelivering, true},
		{domain.StatePending, deliver, "", false},
		{domain.StatePending, retry, "", false},
		{domain.StatePending, fail, "", false},

		{domain.StateDelivering, deliver, domain.StateDelivered, true},
		{domain.StateDelivering, retry, domain.StateRetrying, true},
		{domain.StateDelivering, fail, domain.StateFailed, true},
		{domain.StateDelivering, begin, "", false},

		{domain.StateRetrying, begin, domain.StateDelivering, true},
		{domain.StateRetrying, deliver, "", false},

		// A delivered event is never re-delivered, by any path.
		{domain.StateDelivered, begin, "", false},
		{domain.StateDelivered, retry, "", false},
		{domain.StateDelivered, fail, "", false},

		// The replay path required by the case statement: a definitively
		// failed event can be re-opened.
		{domain.StateFailed, retry, domain.StateRetrying, true},
		{domain.StateFailed, begin, "", false},
		{domain.StateFailed, deliver, "", false},
	}

	for _, tc := range tests {
		t.Run(string(tc.from)+"_"+tc.step.name, func(t *testing.T) {
			event := newEvent(t, tc.from)
			err := tc.step.do(event)

			if tc.allowed {
				if err != nil {
					t.Fatalf("expected transition to be allowed, got %v", err)
				}
				if event.State() != tc.want {
					t.Fatalf("state = %s, want %s", event.State(), tc.want)
				}
				return
			}

			if !errors.Is(err, domain.ErrInvalidTransition) {
				t.Fatalf("expected ErrInvalidTransition, got %v", err)
			}
			if event.State() != tc.from {
				t.Fatalf("rejected transition must not mutate state: got %s, want %s", event.State(), tc.from)
			}
		})
	}
}

func TestCanBeDispatched(t *testing.T) {
	tests := map[domain.EventState]bool{
		domain.StatePending:    true,
		domain.StateRetrying:   true,
		domain.StateDelivering: false,
		domain.StateDelivered:  false,
		domain.StateFailed:     false,
	}

	for state, want := range tests {
		t.Run(string(state), func(t *testing.T) {
			if got := newEvent(t, state).CanBeDispatched(); got != want {
				t.Fatalf("CanBeDispatched() = %v, want %v", got, want)
			}
		})
	}
}

func TestMarkDeliveredClearsLastError(t *testing.T) {
	event := newEvent(t, domain.StateDelivering)
	if err := event.MarkRetrying("timeout", now); err != nil {
		t.Fatal(err)
	}
	if event.LastError() == nil || *event.LastError() != "timeout" {
		t.Fatalf("last error not recorded: %v", event.LastError())
	}

	if err := event.BeginDelivery(now); err != nil {
		t.Fatal(err)
	}
	if err := event.MarkDelivered(now); err != nil {
		t.Fatal(err)
	}
	if event.LastError() != nil {
		t.Fatalf("a delivered event must not keep a stale error: %v", *event.LastError())
	}
}

func TestNewNotificationEventValidation(t *testing.T) {
	payload := json.RawMessage(`{"a":1}`)

	tests := []struct {
		name                         string
		eventID, clientID, eventType string
		payload                      json.RawMessage
		wantErr                      error
	}{
		{name: "missing event id", clientID: "C1", eventType: "t", payload: payload, wantErr: domain.ErrMissingEventID},
		{name: "missing client id", eventID: "E1", eventType: "t", payload: payload, wantErr: domain.ErrMissingClientID},
		{name: "missing event type", eventID: "E1", clientID: "C1", payload: payload, wantErr: domain.ErrMissingEventType},
		{name: "missing payload", eventID: "E1", clientID: "C1", eventType: "t", wantErr: domain.ErrMissingPayload},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewNotificationEvent("id", tc.eventID, tc.clientID, tc.eventType, tc.payload, now)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	event, err := domain.NewNotificationEvent("id", "EVT001", "CLIENT001", "credit_card_payment", payload, now)
	if err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	if event.State() != domain.StatePending {
		t.Fatalf("a freshly ingested event must start PENDING, got %s", event.State())
	}
}
