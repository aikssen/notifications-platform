//go:build integration

package postgres_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/aikssen/notifications-platform/services/retry-service/internal/infrastructure/postgres"
)

// The retry loop's correctness lives almost entirely in SQL, so it is verified
// against a real PostgreSQL running the real schema.
//
//	go test -tags=integration ./...

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

// insertEvent creates a notification event directly, so each test starts from
// the exact row shape it needs.
func insertEvent(t *testing.T, pool *pgxpool.Pool, state string, retryCount int, nextRetryAt any) string {
	t.Helper()
	id := uuid.NewString()

	_, err := pool.Exec(context.Background(), `
		INSERT INTO notification_events (
			id, event_id, client_id, event_type, event_payload,
			state, retry_count, next_retry_at
		) VALUES ($1, $2, 'CLIENT001', 'credit_card_payment', '{"content":"x"}'::jsonb, $3, $4, $5)`,
		id, "EVT-"+id[:8], state, retryCount, nextRetryAt)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func stateOf(t *testing.T, pool *pgxpool.Pool, id string) (string, int, *time.Time) {
	t.Helper()
	var (
		state      string
		retryCount int
		next       *time.Time
	)
	err := pool.QueryRow(context.Background(),
		`SELECT state, retry_count, next_retry_at FROM notification_events WHERE id = $1`, id).
		Scan(&state, &retryCount, &next)
	if err != nil {
		t.Fatal(err)
	}
	return state, retryCount, next
}

func TestClaimDueTakesOnlyEventsThatAreActuallyDue(t *testing.T) {
	pool := startPostgres(t)
	store := postgres.NewRetryStore(pool)
	now := time.Now().UTC()

	dueNow := insertEvent(t, pool, "RETRYING", 1, now.Add(-time.Minute))
	neverScheduled := insertEvent(t, pool, "RETRYING", 0, nil)
	notYetDue := insertEvent(t, pool, "RETRYING", 1, now.Add(time.Hour))
	delivered := insertEvent(t, pool, "DELIVERED", 0, nil)
	alreadyFailed := insertEvent(t, pool, "FAILED", 5, nil)

	claimed, err := store.ClaimDue(context.Background(), now, time.Minute, 50)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, c := range claimed {
		got[c.ID] = true
	}

	if !got[dueNow] {
		t.Error("an event past its next_retry_at must be claimed")
	}
	if !got[neverScheduled] {
		t.Error("an event with no schedule yet must be claimed — it is waiting on us")
	}
	if got[notYetDue] {
		t.Error("an event still inside its backoff window must not be claimed")
	}
	if got[delivered] || got[alreadyFailed] {
		t.Error("terminal events must never be claimed")
	}
}

// FOR UPDATE SKIP LOCKED plus a visibility window is what lets several
// instances of this service run at once: each gets a disjoint batch instead of
// competing for the same rows.
func TestConcurrentPollersGetDisjointBatches(t *testing.T) {
	pool := startPostgres(t)
	store := postgres.NewRetryStore(pool)
	now := time.Now().UTC()

	const events = 40
	for range events {
		insertEvent(t, pool, "RETRYING", 1, now.Add(-time.Minute))
	}

	const pollers = 8
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		all []string
	)

	wg.Add(pollers)
	for range pollers {
		go func() {
			defer wg.Done()
			claimed, err := store.ClaimDue(context.Background(), now, time.Minute, 10)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for _, c := range claimed {
				all = append(all, c.ID)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, id := range all {
		if seen[id] {
			t.Fatalf("event %s was claimed by two pollers at once", id)
		}
		seen[id] = true
	}
	if len(all) == 0 {
		t.Fatal("no events were claimed at all")
	}
}

// The claim itself hides the row, so the database lock does not have to be
// held while publishing to Kafka.
func TestClaimHidesTheEventForTheVisibilityWindow(t *testing.T) {
	pool := startPostgres(t)
	store := postgres.NewRetryStore(pool)
	now := time.Now().UTC()

	id := insertEvent(t, pool, "RETRYING", 1, now.Add(-time.Minute))
	ctx := context.Background()

	first, err := store.ClaimDue(ctx, now, time.Minute, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %d events, err=%v", len(first), err)
	}
	if first[0].RetryCount != 2 {
		t.Fatalf("retry_count = %d, want it incremented to 2 by the claim", first[0].RetryCount)
	}

	// A second poller, immediately afterwards, must see nothing.
	second, err := store.ClaimDue(ctx, now, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatal("a claimed event must stay hidden for the visibility window")
	}

	// Once the window lapses — which is what happens if the process that
	// claimed it died — the event becomes available again.
	afterWindow, err := store.ClaimDue(ctx, now.Add(2*time.Minute), time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterWindow) != 1 || afterWindow[0].ID != id {
		t.Fatal("an expired visibility window must return the event to the queue")
	}
}

func TestScheduleNextDoesNotResurrectADeliveredEvent(t *testing.T) {
	pool := startPostgres(t)
	store := postgres.NewRetryStore(pool)
	ctx := context.Background()

	// The dispatcher delivered it between the claim and this write.
	id := insertEvent(t, pool, "DELIVERED", 1, nil)

	if err := store.ScheduleNext(ctx, id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	state, _, next := stateOf(t, pool, id)
	if state != "DELIVERED" {
		t.Fatalf("state = %s, want DELIVERED", state)
	}
	if next != nil {
		t.Fatal("a delivered event must not be dragged back into a retry schedule")
	}
}

// This is the transition the case statement's replay requirement depends on.
func TestMarkFailedMakesTheEventReplayable(t *testing.T) {
	pool := startPostgres(t)
	store := postgres.NewRetryStore(pool)
	ctx := context.Background()

	id := insertEvent(t, pool, "RETRYING", 5, time.Now().UTC())

	if err := store.MarkFailed(ctx, id, "budget spent", time.Now()); err != nil {
		t.Fatal(err)
	}

	state, _, next := stateOf(t, pool, id)
	if state != "FAILED" {
		t.Fatalf("state = %s, want FAILED", state)
	}
	if next != nil {
		t.Fatal("a failed event must stop matching the poller's index")
	}
}

// Without this sweep, a dispatcher crashing mid-delivery strands the event
// forever: the dispatcher skips DELIVERING and the poller only reads RETRYING.
func TestReclaimStalledRescuesAbandonedDeliveries(t *testing.T) {
	pool := startPostgres(t)
	store := postgres.NewRetryStore(pool)
	ctx := context.Background()

	stalled := insertEvent(t, pool, "DELIVERING", 1, nil)
	_, err := pool.Exec(ctx,
		`UPDATE notification_events SET updated_at = now() - interval '10 minutes' WHERE id = $1`, stalled)
	if err != nil {
		t.Fatal(err)
	}

	// A delivery that started a moment ago is still in flight, not abandoned.
	inFlight := insertEvent(t, pool, "DELIVERING", 1, nil)

	n, err := store.ReclaimStalled(ctx, time.Now().UTC().Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reclaimed %d, want exactly the stalled one", n)
	}

	if state, _, _ := stateOf(t, pool, stalled); state != "RETRYING" {
		t.Fatalf("stalled event is %s, want RETRYING", state)
	}
	if state, _, _ := stateOf(t, pool, inFlight); state != "DELIVERING" {
		t.Fatalf("an in-flight delivery was wrongly reclaimed: %s", state)
	}
}
