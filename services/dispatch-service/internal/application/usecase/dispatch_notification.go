package usecase

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

// DispatchNotification runs one delivery cycle for an already-ingested event.
//
// The shape of this use case is the answer to the case statement's four
// delivery requirements, in order:
//
//  1. confirm with the subscription that the event must be delivered, and that
//     it belongs to the client it claims to belong to;
//  2. deliver to the registered URL;
//  3. hand failures to the retry strategy;
//  4. store the final information about the delivery.
//
// What it deliberately does NOT do is decide whether a failed delivery is
// worth retrying. That is the retry service's policy. Here the only question
// is "did it work?".
type DispatchNotification struct {
	store    port.EventStore
	subs     port.SubscriptionResolver
	webhooks port.WebhookSender
	results  port.ResultPublisher
	clock    port.Clock
	ids      port.IDGenerator
	log      *slog.Logger
}

func NewDispatchNotification(
	store port.EventStore,
	subs port.SubscriptionResolver,
	webhooks port.WebhookSender,
	results port.ResultPublisher,
	clock port.Clock,
	ids port.IDGenerator,
	log *slog.Logger,
) *DispatchNotification {
	return &DispatchNotification{
		store:    store,
		subs:     subs,
		webhooks: webhooks,
		results:  results,
		clock:    clock,
		ids:      ids,
		log:      log,
	}
}

func (uc *DispatchNotification) Execute(
	ctx context.Context,
	event *domain.NotificationEvent,
	source domain.DispatchSource,
	correlationID string,
) error {
	log := uc.log.With(
		slog.String("notification_event_id", event.ID),
		slog.String("event_id", event.EventID),
		slog.String("client_id", event.ClientID),
		slog.String("event_type", event.EventType),
		slog.String("dispatch_source", source.String()),
		slog.String("correlation_id", correlationID),
	)

	observed := event.State()
	if !event.CanBeDispatched() {
		// Not an error: at-least-once means we will legitimately see events
		// that are already delivered or already in flight.
		log.Info("skipping dispatch, event is not in a dispatchable state",
			slog.String("state", observed.String()))
		return nil
	}

	// Claim the event before doing any outbound work. If another instance got
	// there first, this one steps aside rather than delivering twice.
	claimed, err := uc.store.ClaimForDelivery(ctx, event.ID, observed)
	if err != nil {
		return fmt.Errorf("claim event for delivery: %w", err)
	}
	if !claimed {
		log.Info("skipping dispatch, event was claimed by another instance",
			slog.String("state", observed.String()))
		return nil
	}
	if err := event.BeginDelivery(uc.clock.Now()); err != nil {
		return fmt.Errorf("begin delivery: %w", err)
	}

	subscription, err := uc.subs.Resolve(ctx, event.ClientID, event.EventType)
	if err != nil {
		// The subscription service is unavailable. Hand the event back to the
		// retry path instead of losing it.
		return uc.abandonToRetry(ctx, event, source, correlationID, log,
			fmt.Sprintf("subscription lookup failed: %v", err))
	}
	if subscription == nil {
		// The client is not subscribed to this event type. There is nothing to
		// deliver and nothing to retry — but the event stays visible, and stays
		// replayable if they subscribe later.
		return uc.closeWithoutDelivery(ctx, event, source, correlationID, log,
			"no active subscription for client and event type")
	}

	response, sendErr := uc.webhooks.Send(ctx, port.WebhookRequest{
		Subscription: *subscription,
		Payload:      event.Payload,
		Headers:      deliveryHeaders(event, source, correlationID),
	})

	status, outcome := evaluate(*subscription, response, sendErr)

	attempt := domain.NewAttempt(
		uc.ids.NewID(), event, *subscription, source, status, outcome, uc.clock.Now(),
	)

	if status == domain.DeliverySuccess {
		err = event.MarkDelivered(uc.clock.Now())
	} else {
		err = event.MarkRetrying(derefOr(outcome.ErrorMessage, "delivery failed"), uc.clock.Now())
	}
	if err != nil {
		return fmt.Errorf("apply delivery outcome: %w", err)
	}

	// Attempt and state move together. If this fails the Kafka offset is not
	// committed and the event is reprocessed, which is safe: the claim above
	// and the idempotent ingest make a repeat harmless.
	if err := uc.store.RecordOutcome(ctx, event, attempt); err != nil {
		return fmt.Errorf("record delivery outcome: %w", err)
	}

	log.Info("delivery cycle finished",
		slog.String("status", status.String()),
		slog.String("state", event.State().String()),
		slog.Int("attempt_number", attempt.AttemptNumber),
		slog.Int("response_status", derefOr(outcome.ResponseStatus, 0)),
		slog.Int64("duration_ms", outcome.Duration.Milliseconds()),
		slog.Int("http_attempts", response.Attempts),
	)

	uc.publish(ctx, event, attempt, status, source, correlationID, log)
	return nil
}

// evaluate decides success from the client's own declared contract.
func evaluate(
	sub domain.Subscription,
	response port.WebhookResponse,
	sendErr error,
) (domain.DeliveryStatus, domain.AttemptOutcome) {
	outcome := domain.AttemptOutcome{
		ResponseBody: response.Body,
		Duration:     response.Duration,
	}
	if response.Status != 0 {
		status := response.Status
		outcome.ResponseStatus = &status
	}

	if sendErr != nil {
		msg := sendErr.Error()
		outcome.ErrorMessage = &msg
		return domain.DeliveryFailed, outcome
	}

	if !sub.Accepts(response.Status) {
		msg := fmt.Sprintf("webhook returned %d, subscription expects %d",
			response.Status, sub.ExpectedStatus)
		outcome.ErrorMessage = &msg
		return domain.DeliveryFailed, outcome
	}

	return domain.DeliverySuccess, outcome
}

// abandonToRetry releases a claimed event back to the retry path when the
// failure happened before any webhook call was made.
func (uc *DispatchNotification) abandonToRetry(
	ctx context.Context,
	event *domain.NotificationEvent,
	source domain.DispatchSource,
	correlationID string,
	log *slog.Logger,
	reason string,
) error {
	if err := event.MarkRetrying(reason, uc.clock.Now()); err != nil {
		return fmt.Errorf("mark retrying: %w", err)
	}
	if err := uc.store.UpdateState(ctx, event); err != nil {
		return fmt.Errorf("persist retrying state: %w", err)
	}
	log.Warn("delivery aborted before the webhook call, event handed to retry",
		slog.String("reason", reason))
	uc.publish(ctx, event, nil, domain.DeliveryFailed, source, correlationID, log)
	return nil
}

// closeWithoutDelivery ends the lifecycle for an event that can never be
// delivered as things stand. It stays replayable.
func (uc *DispatchNotification) closeWithoutDelivery(
	ctx context.Context,
	event *domain.NotificationEvent,
	source domain.DispatchSource,
	correlationID string,
	log *slog.Logger,
	reason string,
) error {
	if err := event.MarkFailed(reason, uc.clock.Now()); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	if err := uc.store.UpdateState(ctx, event); err != nil {
		return fmt.Errorf("persist failed state: %w", err)
	}
	log.Info("event closed without delivery", slog.String("reason", reason))
	uc.publish(ctx, event, nil, domain.DeliveryFailed, source, correlationID, log)
	return nil
}

// publish emits the delivery result. A monitoring stream that is momentarily
// unavailable must never fail a delivery that already happened, so this only
// logs on error.
func (uc *DispatchNotification) publish(
	ctx context.Context,
	event *domain.NotificationEvent,
	attempt *domain.NotificationAttempt,
	status domain.DeliveryStatus,
	source domain.DispatchSource,
	correlationID string,
	log *slog.Logger,
) {
	result := port.DeliveryResult{
		NotificationEventID: event.ID,
		EventID:             event.EventID,
		ClientID:            event.ClientID,
		EventType:           event.EventType,
		State:               event.State(),
		Status:              status,
		DispatchSource:      source,
		CorrelationID:       correlationID,
		OccurredAt:          uc.clock.Now(),
	}
	if attempt != nil {
		result.AttemptNumber = attempt.AttemptNumber
		result.WebhookURL = attempt.WebhookURL
		result.ResponseStatus = attempt.ResponseStatus
		result.ErrorMessage = attempt.ErrorMessage
		result.DurationMS = derefOr(attempt.DurationMS, 0)
	} else {
		result.ErrorMessage = event.LastError()
	}

	if err := uc.results.Publish(ctx, result); err != nil {
		log.Warn("could not publish delivery result", slog.Any("error", err))
	}
}

func deliveryHeaders(
	event *domain.NotificationEvent,
	source domain.DispatchSource,
	correlationID string,
) map[string]string {
	return map[string]string{
		"X-Notification-Event-Id": event.ID,
		"X-Event-Id":              event.EventID,
		"X-Event-Type":            event.EventType,
		"X-Client-Id":             event.ClientID,
		"X-Dispatch-Source":       source.String(),
		"X-Correlation-Id":        correlationID,
	}
}

func derefOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}
