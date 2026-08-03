package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/usecase"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

func incoming() port.IncomingEvent {
	return port.IncomingEvent{
		EventID:        "EVT001",
		ClientID:       "CLIENT001",
		EventType:      "credit_card_payment",
		Payload:        json.RawMessage(`{"content":"Credit card payment received for $150.00"}`),
		DispatchSource: domain.SourceSystem,
	}
}

func TestIngestCreatesEventWhenAbsent(t *testing.T) {
	store := newFakeStore()
	uc := usecase.NewIngestNotificationEvent(store, fakeClock{}, &seqIDs{})

	event, err := uc.Execute(context.Background(), incoming())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.State() != domain.StatePending {
		t.Fatalf("state = %s, want PENDING", event.State())
	}
	if len(store.inserted) != 1 {
		t.Fatalf("expected one insert, got %d", len(store.inserted))
	}
}

// Idempotency: replaying the same upstream event must not create a second
// notification event.
func TestIngestIsIdempotent(t *testing.T) {
	store := newFakeStore()
	existing := testEvent(domain.StateDelivered)
	store.byEventID["EVT001"] = existing

	uc := usecase.NewIngestNotificationEvent(store, fakeClock{}, &seqIDs{})

	event, err := uc.Execute(context.Background(), incoming())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event != existing {
		t.Fatal("the already-persisted event must be returned as is")
	}
	if len(store.inserted) != 0 {
		t.Fatal("an existing event must not be inserted again")
	}
}

// Two instances can ingest the same event concurrently. The loser must adopt
// the winner's row: continuing with a locally generated id that was never
// persisted is what makes attempts fail their foreign key later.
func TestIngestAdoptsWinnerAfterInsertConflict(t *testing.T) {
	store := newFakeStore()
	store.insertWins = false

	winner := domain.RehydrateNotificationEvent(
		"winner-id", "EVT001", "CLIENT001", "credit_card_payment",
		json.RawMessage(`{"content":"x"}`),
		domain.StatePending, 0, nil, fixedNow, fixedNow,
	)
	// Absent on the first read, present on the retry: exactly what a lost race
	// looks like from this instance.
	store.byEventID = map[string]*domain.NotificationEvent{}
	store.onSecondFind = winner

	uc := usecase.NewIngestNotificationEvent(store, fakeClock{}, &seqIDs{})

	event, err := uc.Execute(context.Background(), incoming())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.ID != "winner-id" {
		t.Fatalf("id = %q, want the persisted winner's id", event.ID)
	}
}

func TestIngestPropagatesStoreFailure(t *testing.T) {
	store := newFakeStore()
	store.findErr = errBoom

	uc := usecase.NewIngestNotificationEvent(store, fakeClock{}, &seqIDs{})

	if _, err := uc.Execute(context.Background(), incoming()); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want it to wrap errBoom", err)
	}
}

func TestIngestRejectsMalformedEvent(t *testing.T) {
	store := newFakeStore()
	uc := usecase.NewIngestNotificationEvent(store, fakeClock{}, &seqIDs{})

	in := incoming()
	in.ClientID = ""

	if _, err := uc.Execute(context.Background(), in); !errors.Is(err, domain.ErrMissingClientID) {
		t.Fatalf("err = %v, want ErrMissingClientID", err)
	}
}
