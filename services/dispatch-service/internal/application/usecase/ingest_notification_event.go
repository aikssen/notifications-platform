package usecase

import (
	"context"
	"fmt"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

// IngestNotificationEvent turns an incoming platform event into a persisted
// notification event, exactly once.
//
// Delivery is at-least-once by design, so this use case is the idempotency
// boundary of the whole service. It is written to survive two instances
// ingesting the same event at the same moment: the loser of the insert race
// re-reads the winning row instead of continuing with an identifier that was
// never persisted.
type IngestNotificationEvent struct {
	store port.EventStore
	clock port.Clock
	ids   port.IDGenerator
}

func NewIngestNotificationEvent(
	store port.EventStore,
	clock port.Clock,
	ids port.IDGenerator,
) *IngestNotificationEvent {
	return &IngestNotificationEvent{store: store, clock: clock, ids: ids}
}

func (uc *IngestNotificationEvent) Execute(
	ctx context.Context,
	in port.IncomingEvent,
) (*domain.NotificationEvent, error) {
	existing, err := uc.store.FindByEventID(ctx, in.EventID)
	if err != nil {
		return nil, fmt.Errorf("look up event %q: %w", in.EventID, err)
	}
	if existing != nil {
		return existing, nil
	}

	event, err := domain.NewNotificationEvent(
		uc.ids.NewID(),
		in.EventID,
		in.ClientID,
		in.EventType,
		in.Payload,
		uc.clock.Now(),
	)
	if err != nil {
		return nil, fmt.Errorf("build event %q: %w", in.EventID, err)
	}

	inserted, err := uc.store.Insert(ctx, event)
	if err != nil {
		return nil, fmt.Errorf("insert event %q: %w", in.EventID, err)
	}
	if inserted {
		return event, nil
	}

	// Another instance won the race. Its row is the truth; ours never existed.
	winner, err := uc.store.FindByEventID(ctx, in.EventID)
	if err != nil {
		return nil, fmt.Errorf("re-read event %q after insert conflict: %w", in.EventID, err)
	}
	if winner == nil {
		return nil, fmt.Errorf("event %q reported as duplicate but cannot be read back", in.EventID)
	}
	return winner, nil
}
