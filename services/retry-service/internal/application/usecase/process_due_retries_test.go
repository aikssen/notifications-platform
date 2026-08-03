package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aikssen/notifications-platform/services/retry-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/application/usecase"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/domain"
)

var (
	fixedNow = time.Date(2026, 3, 15, 9, 30, 22, 0, time.UTC)
	errBoom  = errors.New("boom")
	quiet    = slog.New(slog.NewTextHandler(io.Discard, nil))
)

type fakeClock struct{}

func (fakeClock) Now() time.Time { return fixedNow }

type fixedRandom struct{ v float64 }

func (r fixedRandom) Float64() float64 { return r.v }

type fakeStore struct {
	due []domain.PendingRetry

	claimErr    error
	scheduleErr error
	failErr     error

	claimed    int
	scheduled  map[string]time.Time
	failed     map[string]string
	reclaimed  int
	reclaimCut time.Time
	backlogAge time.Duration
}

func newFakeStore(due ...domain.PendingRetry) *fakeStore {
	return &fakeStore{
		due:       due,
		scheduled: map[string]time.Time{},
		failed:    map[string]string{},
	}
}

func (s *fakeStore) ClaimDue(_ context.Context, _ time.Time, _ time.Duration, limit int) ([]domain.PendingRetry, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	s.claimed++
	if len(s.due) > limit {
		return s.due[:limit], nil
	}
	return s.due, nil
}

func (s *fakeStore) ScheduleNext(_ context.Context, id string, at time.Time) error {
	if s.scheduleErr != nil {
		return s.scheduleErr
	}
	s.scheduled[id] = at
	return nil
}

func (s *fakeStore) MarkFailed(_ context.Context, id, reason string, _ time.Time) error {
	if s.failErr != nil {
		return s.failErr
	}
	s.failed[id] = reason
	return nil
}

func (s *fakeStore) OldestRetryingAge(_ context.Context) (time.Duration, error) {
	return s.backlogAge, nil
}

func (s *fakeStore) ReclaimStalled(_ context.Context, cutoff time.Time) (int, error) {
	s.reclaimCut = cutoff
	return s.reclaimed, nil
}

type fakePublisher struct {
	sent []port.DispatchMessage
	err  error
}

func (p *fakePublisher) Publish(_ context.Context, msg port.DispatchMessage) error {
	if p.err != nil {
		return p.err
	}
	p.sent = append(p.sent, msg)
	return nil
}

type fakeDLQ struct {
	sent []port.DeadLetterRecord
	err  error
}

func (p *fakeDLQ) Publish(_ context.Context, r port.DeadLetterRecord) error {
	if p.err != nil {
		return p.err
	}
	p.sent = append(p.sent, r)
	return nil
}

type harness struct {
	store  *fakeStore
	events *fakePublisher
	dlq    *fakeDLQ
	uc     *usecase.ProcessDueRetries
}

func newHarness(t *testing.T, due ...domain.PendingRetry) *harness {
	t.Helper()

	policy, err := domain.NewRetryPolicy(5, 10*time.Second, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{
		store:  newFakeStore(due...),
		events: &fakePublisher{},
		dlq:    &fakeDLQ{},
	}
	h.uc = usecase.NewProcessDueRetries(
		h.store, h.events, h.dlq, fakeClock{}, fixedRandom{0.5},
		usecase.Options{Policy: policy, Visibility: time.Minute, BatchSize: 50},
		quiet, nil,
	)
	return h
}

func pending(id string, retryCount int) domain.PendingRetry {
	return domain.PendingRetry{
		ID:         id,
		EventID:    "EVT-" + id,
		ClientID:   "CLIENT001",
		EventType:  "credit_card_payment",
		Payload:    json.RawMessage(`{"content":"x"}`),
		RetryCount: retryCount,
	}
}

// A retry re-enters through the same topic as a first delivery. Only the
// origin changes, which is what keeps one delivery pipeline instead of two.
func TestRequeuesThroughTheSamePipeline(t *testing.T) {
	h := newHarness(t, pending("e1", 1))

	if _, err := h.uc.Execute(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(h.events.sent) != 1 {
		t.Fatalf("published %d messages, want 1", len(h.events.sent))
	}
	sent := h.events.sent[0]
	if sent.DispatchSource != domain.DispatchSourceRetry {
		t.Fatalf("dispatch_source = %q, want RETRY_SERVICE", sent.DispatchSource)
	}
	if sent.EventID != "EVT-e1" || sent.ClientID != "CLIENT001" {
		t.Fatalf("message does not carry the original event: %+v", sent)
	}
	if string(sent.EventPayload) != `{"content":"x"}` {
		t.Fatal("a retry must resend the original payload, not a reconstruction")
	}

	next, ok := h.store.scheduled["e1"]
	if !ok {
		t.Fatal("the next attempt was never scheduled")
	}
	if !next.After(fixedNow) {
		t.Fatalf("next retry at %v is not in the future", next)
	}
}

// This is the transition that makes the case statement's replay requirement
// reachable at all.
func TestExhaustedBudgetMakesTheEventReplayable(t *testing.T) {
	h := newHarness(t, pending("e1", 5))

	if _, err := h.uc.Execute(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(h.events.sent) != 0 {
		t.Fatal("an exhausted event must not be requeued")
	}
	reason, failed := h.store.failed["e1"]
	if !failed {
		t.Fatal("the event must be marked FAILED so the client can replay it")
	}
	if reason == "" {
		t.Fatal("the reason must be persisted")
	}
	if len(h.dlq.sent) != 1 {
		t.Fatalf("wrote %d dead letter records, want 1", len(h.dlq.sent))
	}
	if h.dlq.sent[0].RetryCount != 5 {
		t.Fatalf("dead letter record lost the retry count: %+v", h.dlq.sent[0])
	}
}

// If publishing fails the event must NOT be rescheduled, so the visibility
// window expires and it is picked up again.
func TestPublishFailureLeavesTheEventClaimable(t *testing.T) {
	h := newHarness(t, pending("e1", 1))
	h.events.err = errBoom

	if _, err := h.uc.Execute(context.Background()); err != nil {
		t.Fatalf("one bad event must not fail the cycle: %v", err)
	}

	if _, scheduled := h.store.scheduled["e1"]; scheduled {
		t.Fatal("an event that was never published must not be marked as scheduled")
	}
}

// A missing DLQ record is an observability gap, not a lost notification: the
// client-facing state is already correct.
func TestDeadLetterFailureDoesNotBlockTheStateChange(t *testing.T) {
	h := newHarness(t, pending("e1", 5))
	h.dlq.err = errBoom

	if _, err := h.uc.Execute(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, failed := h.store.failed["e1"]; !failed {
		t.Fatal("the event must still be marked FAILED")
	}
}

// The batch is already claimed, so one bad event must not strand the others.
func TestOneBadEventDoesNotAbandonTheBatch(t *testing.T) {
	h := newHarness(t, pending("e1", 1), pending("e2", 1), pending("e3", 1))
	h.store.scheduleErr = errBoom

	claimed, err := h.uc.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claimed != 3 {
		t.Fatalf("claimed = %d, want 3", claimed)
	}
	if len(h.events.sent) != 3 {
		t.Fatalf("published %d, want all 3 attempted despite the failures", len(h.events.sent))
	}
}

func TestNothingDueIsNotAnError(t *testing.T) {
	h := newHarness(t)

	claimed, err := h.uc.Execute(context.Background())
	if err != nil || claimed != 0 {
		t.Fatalf("claimed=%d err=%v, want a quiet no-op", claimed, err)
	}
	if len(h.events.sent) != 0 {
		t.Fatal("nothing should have been published")
	}
}

func TestClaimFailurePropagates(t *testing.T) {
	h := newHarness(t)
	h.store.claimErr = errBoom

	if _, err := h.uc.Execute(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want it to wrap errBoom", err)
	}
}

// The backlog gauge is what tells an on-call engineer that deliveries are
// failing faster than they are being retried — a success-rate percentage alone
// stays flat while the queue grows.
func TestReportsTheBacklogAge(t *testing.T) {
	h := newHarness(t)
	h.store.backlogAge = 7 * time.Minute

	var reported time.Duration
	h.uc = usecase.NewProcessDueRetries(
		h.store, h.events, h.dlq, fakeClock{}, fixedRandom{0.5},
		usecase.Options{Policy: mustPolicy(t), Visibility: time.Minute, BatchSize: 50},
		quiet, &recordingObserver{onBacklog: func(d time.Duration) { reported = d }},
	)

	if err := h.uc.ReportBacklog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reported != 7*time.Minute {
		t.Fatalf("reported %v, want 7m", reported)
	}
}

// Without this sweep, a dispatcher crashing mid-delivery strands the event
// forever: nothing looks at DELIVERING again.
func TestReclaimsDeliveriesAbandonedMidFlight(t *testing.T) {
	h := newHarness(t)
	h.store.reclaimed = 3

	if err := h.uc.ReclaimStalled(context.Background(), 5*time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := fixedNow.Add(-5 * time.Minute)
	if !h.store.reclaimCut.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", h.store.reclaimCut, want)
	}
}

func mustPolicy(t *testing.T) domain.RetryPolicy {
	t.Helper()
	p, err := domain.NewRetryPolicy(5, 10*time.Second, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type recordingObserver struct {
	onBacklog func(time.Duration)
}

func (o *recordingObserver) Requeued(string)                  {}
func (o *recordingObserver) Exhausted(string)                 {}
func (o *recordingObserver) Reclaimed(int)                    {}
func (o *recordingObserver) CycleFinished(int, time.Duration) {}
func (o *recordingObserver) BacklogAge(d time.Duration)       { o.onBacklog(d) }
