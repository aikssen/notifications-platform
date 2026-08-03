package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

const uniqueViolation = "23505"

// attemptNumberRetries bounds the optimistic loop that allocates an attempt
// number. Two dispatchers racing on the same event is normal; ten in a row
// losing the race is a problem worth surfacing.
const attemptNumberRetries = 5

// EventStore is the PostgreSQL adapter for port.EventStore.
type EventStore struct {
	pool *pgxpool.Pool
}

func NewEventStore(pool *pgxpool.Pool) *EventStore {
	return &EventStore{pool: pool}
}

func (s *EventStore) FindByEventID(ctx context.Context, eventID string) (*domain.NotificationEvent, error) {
	const query = `
		SELECT id, event_id, client_id, event_type, event_payload,
		       state, retry_count, last_error, created_at, updated_at
		FROM notification_events
		WHERE event_id = $1`

	var (
		id, evID, clientID, eventType, rawState string
		payload                                 []byte
		retryCount                              int
		lastError                               *string
		createdAt, updatedAt                    time.Time
	)

	err := s.pool.QueryRow(ctx, query, eventID).Scan(
		&id, &evID, &clientID, &eventType, &payload,
		&rawState, &retryCount, &lastError, &createdAt, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query notification event: %w", err)
	}

	state, err := domain.ParseEventState(rawState)
	if err != nil {
		return nil, fmt.Errorf("event %s: %w", id, err)
	}

	return domain.RehydrateNotificationEvent(
		id, evID, clientID, eventType, json.RawMessage(payload),
		state, retryCount, lastError, createdAt, updatedAt,
	), nil
}

// Insert is idempotent by way of the unique constraint on event_id. Reporting
// inserted=false instead of an error is what lets the caller adopt the winning
// row when two instances ingest the same event at once.
func (s *EventStore) Insert(ctx context.Context, event *domain.NotificationEvent) (bool, error) {
	const query = `
		INSERT INTO notification_events (
			id, event_id, client_id, event_type, event_payload,
			state, retry_count, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6, 0, $7, $7)
		ON CONFLICT (event_id) DO NOTHING`

	tag, err := s.pool.Exec(ctx, query,
		event.ID, event.EventID, event.ClientID, event.EventType,
		string(event.Payload), event.State().String(), event.CreatedAt,
	)
	if err != nil {
		return false, fmt.Errorf("insert notification event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimForDelivery is a compare-and-set on the state column.
//
// This is what stops at-least-once delivery from becoming duplicate delivery:
// two dispatchers can read the same PENDING event, but only one UPDATE will
// match the expected state, and only that one goes on to call the webhook.
func (s *EventStore) ClaimForDelivery(
	ctx context.Context,
	notificationEventID string,
	from domain.EventState,
) (bool, error) {
	const query = `
		UPDATE notification_events
		SET state = $3, updated_at = now()
		WHERE id = $1 AND state = $2`

	tag, err := s.pool.Exec(ctx, query,
		notificationEventID, from.String(), domain.StateDelivering.String())
	if err != nil {
		return false, fmt.Errorf("claim event for delivery: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *EventStore) UpdateState(ctx context.Context, event *domain.NotificationEvent) error {
	const query = `
		UPDATE notification_events
		SET state = $2, last_error = $3, updated_at = now()
		WHERE id = $1`

	if _, err := s.pool.Exec(ctx, query,
		event.ID, event.State().String(), event.LastError()); err != nil {
		return fmt.Errorf("update event state: %w", err)
	}
	return nil
}

// RecordOutcome writes the attempt and the resulting event state in one
// transaction.
//
// Splitting these two writes is how an audit trail starts disagreeing with the
// state it is supposed to explain: a crash in between leaves an event marked
// DELIVERED with no record of the delivery, or an attempt with no state change.
func (s *EventStore) RecordOutcome(
	ctx context.Context,
	event *domain.NotificationEvent,
	attempt *domain.NotificationAttempt,
) error {
	var lastErr error

	for try := 0; try < attemptNumberRetries; try++ {
		err := s.recordOutcomeOnce(ctx, event, attempt)
		if err == nil {
			return nil
		}
		if !isUniqueViolation(err) {
			return err
		}
		// Another dispatcher took the attempt number we computed. Recompute it.
		lastErr = err
	}

	return fmt.Errorf("could not allocate an attempt number after %d tries: %w",
		attemptNumberRetries, lastErr)
}

func (s *EventStore) recordOutcomeOnce(
	ctx context.Context,
	event *domain.NotificationEvent,
	attempt *domain.NotificationAttempt,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the parent event row first.
	//
	// Without this, several dispatchers writing attempts for the same event all
	// read the same MAX(attempt_number) under READ COMMITTED, all compute the
	// same next value, and all but one lose to the unique index — a thundering
	// herd that needs as many retries as there are writers. Taking the row lock
	// serialises attempt numbering for this event and only this event:
	// attempts for other events are completely unaffected.
	//
	// The event row is updated at the end of this transaction anyway, so the
	// lock costs nothing extra.
	const lockEvent = `SELECT 1 FROM notification_events WHERE id = $1 FOR UPDATE`
	if _, err := tx.Exec(ctx, lockEvent, event.ID); err != nil {
		return fmt.Errorf("lock notification event: %w", err)
	}

	// With the row locked, MAX(attempt_number) is stable for the duration of
	// the INSERT. The unique index remains as a backstop.
	const insertAttempt = `
		INSERT INTO notification_attempts (
			id, notification_event_id, attempt_number, dispatch_source, status,
			webhook_url, request_method, request_payload,
			response_status, response_body, error_message, duration_ms, attempted_at
		)
		SELECT $1, $2, COALESCE(MAX(attempt_number), 0) + 1, $3, $4,
		       $5, $6, $7::jsonb,
		       $8, $9::jsonb, $10, $11, $12
		FROM notification_attempts
		WHERE notification_event_id = $2
		RETURNING attempt_number`

	var assigned int
	err = tx.QueryRow(ctx, insertAttempt,
		attempt.ID,
		attempt.NotificationEventID,
		attempt.DispatchSource.String(),
		attempt.Status.String(),
		attempt.WebhookURL,
		attempt.RequestMethod,
		string(attempt.RequestPayload),
		attempt.ResponseStatus,
		nullableJSON(attempt.ResponseBody),
		attempt.ErrorMessage,
		attempt.DurationMS,
		attempt.AttemptedAt,
	).Scan(&assigned)
	if err != nil {
		return fmt.Errorf("insert notification attempt: %w", err)
	}

	const updateEvent = `
		UPDATE notification_events
		SET state = $2, last_error = $3, updated_at = now()
		WHERE id = $1`

	if _, err := tx.Exec(ctx, updateEvent,
		event.ID, event.State().String(), event.LastError()); err != nil {
		return fmt.Errorf("update event state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delivery outcome: %w", err)
	}

	attempt.AttemptNumber = assigned
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// nullableJSON keeps an empty body as SQL NULL rather than an invalid JSONB
// value.
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

var _ port.EventStore = (*EventStore)(nil)
