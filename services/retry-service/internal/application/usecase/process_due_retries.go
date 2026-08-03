package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aikssen/notifications-platform/services/retry-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/domain"
)

// Observer lets the use case report what it did without importing a metrics
// library.
type Observer interface {
	Requeued(eventType string)
	Exhausted(eventType string)
	Reclaimed(n int)
	CycleFinished(claimed int, d time.Duration)
	BacklogAge(d time.Duration)
}

// ProcessDueRetries runs one pass of the retry loop.
type ProcessDueRetries struct {
	store      port.RetryStore
	events     port.EventPublisher
	deadLetter port.DeadLetterPublisher
	clock      port.Clock
	random     port.Randomizer
	policy     domain.RetryPolicy
	log        *slog.Logger
	observer   Observer

	visibility time.Duration
	batchSize  int
}

type Options struct {
	Policy     domain.RetryPolicy
	Visibility time.Duration
	BatchSize  int
}

func NewProcessDueRetries(
	store port.RetryStore,
	events port.EventPublisher,
	deadLetter port.DeadLetterPublisher,
	clock port.Clock,
	random port.Randomizer,
	opts Options,
	log *slog.Logger,
	observer Observer,
) *ProcessDueRetries {
	return &ProcessDueRetries{
		store:      store,
		events:     events,
		deadLetter: deadLetter,
		clock:      clock,
		random:     random,
		policy:     opts.Policy,
		log:        log,
		observer:   observer,
		visibility: opts.Visibility,
		batchSize:  opts.BatchSize,
	}
}

// Execute claims everything due and acts on it. It returns how many events
// were claimed, so the caller can poll faster while a backlog is draining.
func (uc *ProcessDueRetries) Execute(ctx context.Context) (int, error) {
	started := uc.clock.Now()

	due, err := uc.store.ClaimDue(ctx, started, uc.visibility, uc.batchSize)
	if err != nil {
		return 0, fmt.Errorf("claim due retries: %w", err)
	}
	if len(due) == 0 {
		return 0, nil
	}

	uc.log.Info("claimed events for retry", slog.Int("count", len(due)))

	for _, event := range due {
		decision := domain.Decide(event, uc.policy, uc.random.Float64(), uc.clock.Now())

		// One event failing must not abandon the rest of the batch: they are
		// already claimed, and dropping out here would leave them waiting for
		// the visibility window to expire.
		if err := uc.apply(ctx, decision); err != nil {
			uc.log.Error("could not apply the retry decision",
				slog.String("notification_event_id", event.ID),
				slog.String("event_id", event.EventID),
				slog.Any("error", err),
			)
		}

		if ctx.Err() != nil {
			break
		}
	}

	if uc.observer != nil {
		uc.observer.CycleFinished(len(due), uc.clock.Now().Sub(started))
	}
	return len(due), nil
}

// ReportBacklog publishes how long the oldest undelivered event has waited.
// Called on its own cadence, since it is a gauge rather than a counter and
// costs one aggregate query.
func (uc *ProcessDueRetries) ReportBacklog(ctx context.Context) error {
	age, err := uc.store.OldestRetryingAge(ctx)
	if err != nil {
		return fmt.Errorf("report backlog: %w", err)
	}
	if uc.observer != nil {
		uc.observer.BacklogAge(age)
	}
	return nil
}

func (uc *ProcessDueRetries) apply(ctx context.Context, d domain.Decision) error {
	log := uc.log.With(
		slog.String("notification_event_id", d.Event.ID),
		slog.String("event_id", d.Event.EventID),
		slog.String("client_id", d.Event.ClientID),
		slog.String("event_type", d.Event.EventType),
		slog.Int("retry_count", d.Event.RetryCount),
	)

	if d.Outcome == domain.Exhaust {
		return uc.exhaust(ctx, d, log)
	}
	return uc.requeue(ctx, d, log)
}

// requeue publishes first and schedules second.
//
// The order matters. If publishing succeeds and scheduling then fails, the
// event is delivered and simply becomes due again when the visibility window
// expires — a duplicate delivery attempt, which idempotency and the
// dispatcher's claim already absorb. The reverse order risks the opposite:
// an event marked as scheduled that was never actually published.
func (uc *ProcessDueRetries) requeue(ctx context.Context, d domain.Decision, log *slog.Logger) error {
	msg := port.DispatchMessage{
		EventID:      d.Event.EventID,
		ClientID:     d.Event.ClientID,
		EventType:    d.Event.EventType,
		EventPayload: d.Event.Payload,
		// The identical pipeline as a first delivery. Only the origin differs,
		// and only so the audit trail can tell them apart.
		DispatchSource: domain.DispatchSourceRetry,
	}

	if err := uc.events.Publish(ctx, msg); err != nil {
		return fmt.Errorf("republish event: %w", err)
	}

	if err := uc.store.ScheduleNext(ctx, d.Event.ID, d.NextRetryAt); err != nil {
		return fmt.Errorf("schedule next retry: %w", err)
	}

	log.Info("event requeued for delivery",
		slog.Time("next_retry_at", d.NextRetryAt))

	if uc.observer != nil {
		uc.observer.Requeued(d.Event.EventType)
	}
	return nil
}

func (uc *ProcessDueRetries) exhaust(ctx context.Context, d domain.Decision, log *slog.Logger) error {
	// The database is the source of truth for the client-facing state, so it
	// is written first. The DLQ topic is for alerting and recovery tooling.
	if err := uc.store.MarkFailed(ctx, d.Event.ID, d.Reason, uc.clock.Now()); err != nil {
		return fmt.Errorf("mark event failed: %w", err)
	}

	record := port.DeadLetterRecord{
		NotificationEventID: d.Event.ID,
		EventID:             d.Event.EventID,
		ClientID:            d.Event.ClientID,
		EventType:           d.Event.EventType,
		EventPayload:        d.Event.Payload,
		RetryCount:          d.Event.RetryCount,
		Reason:              d.Reason,
		DeadAt:              uc.clock.Now(),
	}
	if err := uc.deadLetter.Publish(ctx, record); err != nil {
		// The client-facing state is already correct and the event is
		// replayable; a missing DLQ record is an observability gap, not a lost
		// notification.
		log.Warn("could not write the dead letter record", slog.Any("error", err))
	}

	log.Warn("retry budget exhausted, the event is now replayable by the client",
		slog.String("reason", d.Reason))

	if uc.observer != nil {
		uc.observer.Exhausted(d.Event.EventType)
	}
	return nil
}

// ReclaimStalled rescues events abandoned mid-delivery.
//
// A dispatcher that dies after claiming an event leaves it in DELIVERING, where
// nothing will ever touch it again: the dispatcher skips DELIVERING events, and
// the retry poller only looks at RETRYING ones. Without this sweep, a single
// crash silently strands a client's notification forever.
func (uc *ProcessDueRetries) ReclaimStalled(ctx context.Context, after time.Duration) error {
	cutoff := uc.clock.Now().Add(-after)

	n, err := uc.store.ReclaimStalled(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("reclaim stalled deliveries: %w", err)
	}
	if n == 0 {
		return nil
	}

	uc.log.Warn("reclaimed deliveries abandoned mid-flight",
		slog.Int("count", n),
		slog.Time("stalled_before", cutoff),
	)
	if uc.observer != nil {
		uc.observer.Reclaimed(n)
	}
	return nil
}
