//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/postgres"
)

// These tests run against a real PostgreSQL with the real schema. The unit
// tests prove the rules; these prove that the SQL implementing them actually
// behaves the way the rules assume — particularly the two pieces of
// concurrency control that a mock can never validate.
//
//	go test -tags=integration ./...

var fixedNow = time.Date(2026, 3, 15, 9, 30, 22, 0, time.UTC)

func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	schema, err := filepath.Abs("../../../../../deploy/postgres/init/001_schema.sql")
	if err != nil {
		t.Fatal(err)
	}

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("notifications"),
		tcpostgres.WithUsername("notifications"),
		tcpostgres.WithPassword("notifications"),
		// The very same file the platform deploys, not a copy that can drift.
		tcpostgres.WithInitScripts(schema),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}

	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func newEvent(t *testing.T, store *postgres.EventStore, eventID string) *domain.NotificationEvent {
	t.Helper()
	// The internal id is ours and is a real UUID; only the upstream identifiers
	// are opaque strings.
	event, err := domain.NewNotificationEvent(
		uuid.NewString(), eventID, "CLIENT001", "credit_card_payment",
		json.RawMessage(`{"content":"Credit card payment received for $150.00"}`),
		fixedNow,
	)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := store.Insert(context.Background(), event)
	if err != nil || !inserted {
		t.Fatalf("insert: inserted=%v err=%v", inserted, err)
	}
	return event
}

// The fixture from the case statement uses EVT001 / CLIENT001. Storing
// upstream identifiers as opaque strings is what lets it load verbatim.
func TestStoresUpstreamIdentifiersVerbatim(t *testing.T) {
	store := postgres.NewEventStore(startPostgres(t))
	newEvent(t, store, "EVT001")

	found, err := store.FindByEventID(context.Background(), "EVT001")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("event not found")
	}
	if found.EventID != "EVT001" || found.ClientID != "CLIENT001" {
		t.Fatalf("identifiers were altered: %s / %s", found.EventID, found.ClientID)
	}
}

// Idempotency, enforced by the database rather than by a read-then-write check
// in application code.
func TestInsertIsIdempotent(t *testing.T) {
	store := postgres.NewEventStore(startPostgres(t))
	first := newEvent(t, store, "EVT002")

	duplicate, err := domain.NewNotificationEvent(
		uuid.NewString(), "EVT002", "CLIENT001", "credit_card_payment",
		json.RawMessage(`{"content":"duplicate"}`), fixedNow,
	)
	if err != nil {
		t.Fatal(err)
	}

	inserted, err := store.Insert(context.Background(), duplicate)
	if err != nil {
		t.Fatalf("a duplicate must not be an error: %v", err)
	}
	if inserted {
		t.Fatal("the duplicate must not have been inserted")
	}

	found, _ := store.FindByEventID(context.Background(), "EVT002")
	if found.ID != first.ID {
		t.Fatalf("the original row must win: got %s, want %s", found.ID, first.ID)
	}
}

// Exactly one of several concurrent dispatchers may deliver an event. This is
// the compare-and-set that turns at-least-once consumption into at-most-once
// delivery.
func TestOnlyOneDispatcherCanClaimAnEvent(t *testing.T) {
	store := postgres.NewEventStore(startPostgres(t))
	event := newEvent(t, store, "EVT003")

	const racers = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int
	)

	wg.Add(racers)
	for range racers {
		go func() {
			defer wg.Done()
			ok, err := store.ClaimForDelivery(context.Background(), event.ID, domain.StatePending)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if granted != 1 {
		t.Fatalf("%d dispatchers claimed the same event, want exactly 1", granted)
	}
}

func TestClaimRejectsAnUnexpectedState(t *testing.T) {
	store := postgres.NewEventStore(startPostgres(t))
	event := newEvent(t, store, "EVT004")

	ctx := context.Background()
	if ok, _ := store.ClaimForDelivery(ctx, event.ID, domain.StatePending); !ok {
		t.Fatal("the first claim should succeed")
	}
	// The event is DELIVERING now, so a claim expecting PENDING must fail.
	if ok, _ := store.ClaimForDelivery(ctx, event.ID, domain.StatePending); ok {
		t.Fatal("a stale expectation must not be able to claim the event")
	}
}

// The previous implementation computed MAX(attempt_number)+1 in one query and
// inserted in another, which loses rows under concurrency against the unique
// index. Deriving the number inside the INSERT, plus a retry on violation,
// makes the sequence hold.
func TestAttemptNumbersAreUniqueAndContiguousUnderConcurrency(t *testing.T) {
	pool := startPostgres(t)
	store := postgres.NewEventStore(pool)
	event := newEvent(t, store, "EVT005")

	sub := domain.Subscription{
		WebhookURL:     "https://client.example.com/hooks",
		HTTPMethod:     "POST",
		ExpectedStatus: 200,
	}

	const concurrent = 15
	var wg sync.WaitGroup
	wg.Add(concurrent)

	for i := range concurrent {
		go func(i int) {
			defer wg.Done()

			// Each writer needs its own in-memory copy: they all move the same
			// row, and the point here is the attempt sequence.
			local := domain.RehydrateNotificationEvent(
				event.ID, event.EventID, event.ClientID, event.EventType,
				event.Payload, domain.StateDelivering, 0, nil, fixedNow, fixedNow,
			)
			if err := local.MarkRetrying("concurrent write", fixedNow); err != nil {
				t.Error(err)
				return
			}

			attempt := domain.NewAttempt(
				uuid.NewString(), local, sub, domain.SourceSystem, domain.DeliveryFailed,
				domain.AttemptOutcome{Duration: time.Millisecond}, fixedNow,
			)

			if err := store.RecordOutcome(context.Background(), local, attempt); err != nil {
				t.Errorf("record outcome %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	var count, distinct, maxNumber int
	err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*), COUNT(DISTINCT attempt_number), COALESCE(MAX(attempt_number), 0)
		FROM notification_attempts WHERE notification_event_id = $1`, event.ID).
		Scan(&count, &distinct, &maxNumber)
	if err != nil {
		t.Fatal(err)
	}

	if count != concurrent {
		t.Fatalf("persisted %d attempts, want %d — writes were lost", count, concurrent)
	}
	if distinct != concurrent {
		t.Fatalf("%d distinct attempt numbers for %d attempts — the sequence collided", distinct, concurrent)
	}
	if maxNumber != concurrent {
		t.Fatalf("highest attempt number is %d, want %d — the sequence has gaps", maxNumber, concurrent)
	}
}

// The attempt and the state change are one fact. A reader must never see an
// event marked DELIVERED with no record of the delivery.
func TestRecordOutcomeIsAtomic(t *testing.T) {
	pool := startPostgres(t)
	store := postgres.NewEventStore(pool)
	event := newEvent(t, store, "EVT006")

	ctx := context.Background()
	if _, err := store.ClaimForDelivery(ctx, event.ID, domain.StatePending); err != nil {
		t.Fatal(err)
	}
	if err := event.BeginDelivery(fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := event.MarkDelivered(fixedNow); err != nil {
		t.Fatal(err)
	}

	status := 200
	attempt := domain.NewAttempt(
		uuid.NewString(), event,
		domain.Subscription{WebhookURL: "https://client.example.com/hooks", HTTPMethod: "POST"},
		domain.SourceSystem, domain.DeliverySuccess,
		domain.AttemptOutcome{ResponseStatus: &status, Duration: 42 * time.Millisecond},
		fixedNow,
	)

	if err := store.RecordOutcome(ctx, event, attempt); err != nil {
		t.Fatal(err)
	}
	if attempt.AttemptNumber != 1 {
		t.Fatalf("attempt number = %d, want 1", attempt.AttemptNumber)
	}

	var state string
	var attempts int
	if err := pool.QueryRow(ctx, `
		SELECT ne.state, COUNT(na.id)
		FROM notification_events ne
		LEFT JOIN notification_attempts na ON na.notification_event_id = ne.id
		WHERE ne.id = $1 GROUP BY ne.state`, event.ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}

	if state != "DELIVERED" || attempts != 1 {
		t.Fatalf("state=%s attempts=%d, want DELIVERED with exactly one attempt", state, attempts)
	}
}
