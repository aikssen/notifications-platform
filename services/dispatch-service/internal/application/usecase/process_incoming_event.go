package usecase

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
)

// ProcessIncomingEvent is the inbound port implementation: ingest, then
// dispatch. It is the only thing the Kafka adapter knows about.
type ProcessIncomingEvent struct {
	ingest   *IngestNotificationEvent
	dispatch *DispatchNotification
	ids      port.IDGenerator
	log      *slog.Logger
}

func NewProcessIncomingEvent(
	ingest *IngestNotificationEvent,
	dispatch *DispatchNotification,
	ids port.IDGenerator,
	log *slog.Logger,
) *ProcessIncomingEvent {
	return &ProcessIncomingEvent{ingest: ingest, dispatch: dispatch, ids: ids, log: log}
}

// Process handles one delivery request end to end.
//
// Returning an error matters: the consumer adapter does not commit the offset
// when this fails, so the message is redelivered rather than silently dropped.
func (uc *ProcessIncomingEvent) Process(ctx context.Context, in port.IncomingEvent) error {
	if in.CorrelationID == "" {
		in.CorrelationID = uc.ids.NewID()
	}

	event, err := uc.ingest.Execute(ctx, in)
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}

	if err := uc.dispatch.Execute(ctx, event, in.DispatchSource, in.CorrelationID); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	return nil
}

var _ port.EventProcessor = (*ProcessIncomingEvent)(nil)
