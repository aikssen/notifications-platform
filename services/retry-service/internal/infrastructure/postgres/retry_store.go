package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aikssen/notifications-platform/services/retry-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/domain"
)

type RetryStore struct {
	pool *pgxpool.Pool
}

func NewRetryStore(pool *pgxpool.Pool) *RetryStore {
	return &RetryStore{pool: pool}
}

// ClaimDue takes ownership of the events that are due for another attempt.
//
// Two details make this safe to run on several instances at once:
//
//	FOR UPDATE SKIP LOCKED — concurrent pollers walk past rows another poller
//	is already reading instead of blocking behind them. Each instance gets a
//	disjoint batch, and adding instances adds throughput rather than contention.
//
//	next_retry_at pushed forward by the visibility window — the claim itself
//	hides the row from the next poll, so the database lock does not have to be
//	held while publishing to Kafka. If this process dies before it finishes,
//	the window lapses and the event becomes due again on its own.
//
// The partial index on (next_retry_at) WHERE state = 'RETRYING' means this
// scans only the rows that are actually waiting, not the whole table.
func (s *RetryStore) ClaimDue(
	ctx context.Context,
	now time.Time,
	visibility time.Duration,
	limit int,
) ([]domain.PendingRetry, error) {
	const query = `
		UPDATE notification_events
		SET retry_count   = retry_count + 1,
		    next_retry_at = $1::timestamptz + $2::interval,
		    updated_at    = now()
		WHERE id IN (
			SELECT id
			FROM notification_events
			WHERE state = 'RETRYING'
			  AND (next_retry_at IS NULL OR next_retry_at <= $1)
			ORDER BY next_retry_at NULLS FIRST
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, event_id, client_id, event_type, event_payload, retry_count, last_error`

	rows, err := s.pool.Query(ctx, query, now, visibility, limit)
	if err != nil {
		return nil, fmt.Errorf("claim due events: %w", err)
	}
	defer rows.Close()

	var claimed []domain.PendingRetry
	for rows.Next() {
		var (
			e       domain.PendingRetry
			payload []byte
		)
		if err := rows.Scan(
			&e.ID, &e.EventID, &e.ClientID, &e.EventType,
			&payload, &e.RetryCount, &e.LastError,
		); err != nil {
			return nil, fmt.Errorf("scan claimed event: %w", err)
		}
		e.Payload = json.RawMessage(payload)
		claimed = append(claimed, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read claimed events: %w", err)
	}

	return claimed, nil
}

func (s *RetryStore) ScheduleNext(ctx context.Context, notificationEventID string, at time.Time) error {
	// Guarded on the state: if the dispatcher has already delivered the event
	// between the claim and this write, we must not drag it back into a retry
	// schedule.
	const query = `
		UPDATE notification_events
		SET next_retry_at = $2, updated_at = now()
		WHERE id = $1 AND state = 'RETRYING'`

	if _, err := s.pool.Exec(ctx, query, notificationEventID, at); err != nil {
		return fmt.Errorf("schedule next retry: %w", err)
	}
	return nil
}

// MarkFailed closes an event whose retry budget is spent.
//
// next_retry_at is cleared so the row stops matching the poller's index, and
// the state becomes the one the self-service replay endpoint accepts.
func (s *RetryStore) MarkFailed(
	ctx context.Context,
	notificationEventID, reason string,
	_ time.Time,
) error {
	const query = `
		UPDATE notification_events
		SET state = 'FAILED', last_error = $2, next_retry_at = NULL, updated_at = now()
		WHERE id = $1 AND state = 'RETRYING'`

	if _, err := s.pool.Exec(ctx, query, notificationEventID, reason); err != nil {
		return fmt.Errorf("mark event failed: %w", err)
	}
	return nil
}

// OldestRetryingAge measures from created_at rather than from the last
// attempt: what matters operationally is how long the client has been waiting
// for a notification, not how recently we last tried.
func (s *RetryStore) OldestRetryingAge(ctx context.Context) (time.Duration, error) {
	const query = `
		SELECT COALESCE(EXTRACT(EPOCH FROM (now() - MIN(created_at))), 0)
		FROM notification_events
		WHERE state IN ('RETRYING', 'DELIVERING')`

	var seconds float64
	if err := s.pool.QueryRow(ctx, query).Scan(&seconds); err != nil {
		return 0, fmt.Errorf("measure retry backlog: %w", err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// ReclaimStalled rescues events left in DELIVERING by a dispatcher that died
// mid-delivery.
//
// The dispatcher will not touch a DELIVERING event — that is precisely how it
// avoids delivering twice — and this poller only looks at RETRYING ones. So
// without this sweep, one crashed process strands a client's notification
// permanently.
func (s *RetryStore) ReclaimStalled(ctx context.Context, olderThan time.Time) (int, error) {
	const query = `
		UPDATE notification_events
		SET state         = 'RETRYING',
		    last_error    = COALESCE(last_error, 'delivery abandoned mid-flight'),
		    next_retry_at = NULL,
		    updated_at    = now()
		WHERE state = 'DELIVERING' AND updated_at < $1`

	tag, err := s.pool.Exec(ctx, query, olderThan)
	if err != nil {
		return 0, fmt.Errorf("reclaim stalled deliveries: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

var _ port.RetryStore = (*RetryStore)(nil)
