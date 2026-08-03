package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

// These fakes are the point of the architecture: every use case below is
// exercised with no Kafka, no PostgreSQL and no HTTP server.

var (
	fixedNow  = time.Date(2026, 3, 15, 9, 30, 22, 0, time.UTC)
	errBoom   = errors.New("boom")
	testQuiet = slog.New(slog.NewTextHandler(io.Discard, nil))
)

type fakeClock struct{}

func (fakeClock) Now() time.Time { return fixedNow }

type seqIDs struct {
	mu sync.Mutex
	n  int
}

func (g *seqIDs) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return "id-" + strconv.Itoa(g.n)
}

// ---------------------------------------------------------------------------

type fakeStore struct {
	byEventID map[string]*domain.NotificationEvent

	// behaviour switches
	findErr        error
	insertErr      error
	insertWins     bool // false => Insert reports a duplicate
	claimGranted   bool
	claimErr       error
	recordErr      error
	updateStateErr error

	// onSecondFind simulates losing an insert race: absent on the first read,
	// present once the winner has committed.
	onSecondFind *domain.NotificationEvent
	finds        int

	// recorded calls
	inserted     []*domain.NotificationEvent
	outcomes     []*domain.NotificationAttempt
	stateUpdates []domain.EventState
	claims       int
	nextAttempt  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byEventID:    map[string]*domain.NotificationEvent{},
		insertWins:   true,
		claimGranted: true,
	}
}

func (s *fakeStore) FindByEventID(_ context.Context, eventID string) (*domain.NotificationEvent, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	s.finds++
	if s.onSecondFind != nil && s.finds > 1 {
		return s.onSecondFind, nil
	}
	return s.byEventID[eventID], nil
}

func (s *fakeStore) Insert(_ context.Context, event *domain.NotificationEvent) (bool, error) {
	if s.insertErr != nil {
		return false, s.insertErr
	}
	if !s.insertWins {
		return false, nil
	}
	s.inserted = append(s.inserted, event)
	s.byEventID[event.EventID] = event
	return true, nil
}

func (s *fakeStore) ClaimForDelivery(_ context.Context, _ string, _ domain.EventState) (bool, error) {
	s.claims++
	if s.claimErr != nil {
		return false, s.claimErr
	}
	return s.claimGranted, nil
}

func (s *fakeStore) RecordOutcome(_ context.Context, event *domain.NotificationEvent, attempt *domain.NotificationAttempt) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.nextAttempt++
	attempt.AttemptNumber = s.nextAttempt
	s.outcomes = append(s.outcomes, attempt)
	s.stateUpdates = append(s.stateUpdates, event.State())
	return nil
}

func (s *fakeStore) UpdateState(_ context.Context, event *domain.NotificationEvent) error {
	if s.updateStateErr != nil {
		return s.updateStateErr
	}
	s.stateUpdates = append(s.stateUpdates, event.State())
	return nil
}

// ---------------------------------------------------------------------------

type fakeResolver struct {
	subscription *domain.Subscription
	err          error
	calls        int
}

func (r *fakeResolver) Resolve(_ context.Context, _, _ string) (*domain.Subscription, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.subscription, nil
}

// ---------------------------------------------------------------------------

type fakeSender struct {
	response port.WebhookResponse
	err      error
	calls    int
	lastReq  port.WebhookRequest
}

func (s *fakeSender) Send(_ context.Context, req port.WebhookRequest) (port.WebhookResponse, error) {
	s.calls++
	s.lastReq = req
	return s.response, s.err
}

// ---------------------------------------------------------------------------

type fakePublisher struct {
	published []port.DeliveryResult
	err       error
}

func (p *fakePublisher) Publish(_ context.Context, r port.DeliveryResult) error {
	if p.err != nil {
		return p.err
	}
	p.published = append(p.published, r)
	return nil
}

// ---------------------------------------------------------------------------

func testEvent(state domain.EventState) *domain.NotificationEvent {
	return domain.RehydrateNotificationEvent(
		"nev-1", "EVT001", "CLIENT001", "credit_card_payment",
		json.RawMessage(`{"content":"Credit card payment received for $150.00"}`),
		state, 0, nil, fixedNow, fixedNow,
	)
}

func webhookOK(status int) port.WebhookResponse {
	return port.WebhookResponse{
		Status:   status,
		Body:     json.RawMessage(`{"status":"received"}`),
		Duration: 42 * time.Millisecond,
		Attempts: 1,
	}
}

func testSubscription(expectedStatus int) *domain.Subscription {
	return &domain.Subscription{
		ID:             "sub-1",
		ClientID:       "CLIENT001",
		EventType:      "credit_card_payment",
		WebhookURL:     "https://client.example.com/hooks/payments",
		HTTPMethod:     "POST",
		ExpectedStatus: expectedStatus,
		HMACSecret:     "secret",
	}
}
