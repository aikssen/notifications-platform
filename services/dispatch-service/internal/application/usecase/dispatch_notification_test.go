package usecase_test

import (
	"context"
	"testing"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/usecase"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

type harness struct {
	store     *fakeStore
	resolver  *fakeResolver
	sender    *fakeSender
	publisher *fakePublisher
	uc        *usecase.DispatchNotification
}

func newHarness() *harness {
	h := &harness{
		store:     newFakeStore(),
		resolver:  &fakeResolver{subscription: testSubscription(200)},
		sender:    &fakeSender{},
		publisher: &fakePublisher{},
	}
	h.uc = usecase.NewDispatchNotification(
		h.store, h.resolver, h.sender, h.publisher,
		fakeClock{}, &seqIDs{}, testQuiet,
	)
	return h
}

func TestDeliversAndMarksDelivered(t *testing.T) {
	h := newHarness()
	h.sender.response = webhookOK(200)
	event := testEvent(domain.StatePending)

	if err := h.uc.Execute(context.Background(), event, domain.SourceSystem, "corr-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.State() != domain.StateDelivered {
		t.Fatalf("state = %s, want DELIVERED", event.State())
	}
	if len(h.store.outcomes) != 1 {
		t.Fatalf("expected exactly one persisted attempt, got %d", len(h.store.outcomes))
	}
	if got := h.store.outcomes[0].Status; got != domain.DeliverySuccess {
		t.Fatalf("attempt status = %s, want SUCCESS", got)
	}
	if len(h.publisher.published) != 1 {
		t.Fatalf("expected the delivery result to be published once, got %d", len(h.publisher.published))
	}
}

// The previous implementation left an exhausted failure in PENDING, which made
// FAILED unreachable and the retry path dead. A failed delivery must hand the
// event to the retry path.
func TestFailedDeliveryGoesToRetryingNotPending(t *testing.T) {
	h := newHarness()
	h.sender.response = webhookOK(500)
	event := testEvent(domain.StatePending)

	if err := h.uc.Execute(context.Background(), event, domain.SourceSystem, "corr-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.State() != domain.StateRetrying {
		t.Fatalf("state = %s, want RETRYING", event.State())
	}
	if h.store.outcomes[0].Status != domain.DeliveryFailed {
		t.Fatal("attempt should have been recorded as FAILED")
	}
	if h.store.outcomes[0].ResponseStatus == nil || *h.store.outcomes[0].ResponseStatus != 500 {
		t.Fatal("the response status must survive into the audit trail")
	}
}

// The subscription declares the status that means "accepted". A 200 against a
// subscription expecting 201 is a failed delivery, not a success.
func TestHonoursExpectedStatusFromSubscription(t *testing.T) {
	h := newHarness()
	h.resolver.subscription = testSubscription(201)
	h.sender.response = webhookOK(200)
	event := testEvent(domain.StatePending)

	if err := h.uc.Execute(context.Background(), event, domain.SourceSystem, "corr-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.State() != domain.StateRetrying {
		t.Fatalf("state = %s, want RETRYING — 200 does not satisfy an expected 201", event.State())
	}
	if h.store.outcomes[0].ErrorMessage == nil {
		t.Fatal("the mismatch should be explained in the audit trail")
	}
}

func TestTransportErrorIsRecordedAndRetried(t *testing.T) {
	h := newHarness()
	h.sender.err = errBoom
	event := testEvent(domain.StatePending)

	if err := h.uc.Execute(context.Background(), event, domain.SourceSystem, "corr-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.State() != domain.StateRetrying {
		t.Fatalf("state = %s, want RETRYING", event.State())
	}
	if msg := h.store.outcomes[0].ErrorMessage; msg == nil || *msg != errBoom.Error() {
		t.Fatalf("error message = %v, want %q", msg, errBoom.Error())
	}
}

// "It is mandatory to ensure notifications sent to every client belong to
// events generated to that client." No subscription means no delivery — and
// crucially, no webhook call at all.
func TestNoSubscriptionClosesEventWithoutCallingWebhook(t *testing.T) {
	h := newHarness()
	h.resolver.subscription = nil
	event := testEvent(domain.StatePending)

	if err := h.uc.Execute(context.Background(), event, domain.SourceSystem, "corr-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.sender.calls != 0 {
		t.Fatal("no subscription must mean no outbound call")
	}
	if event.State() != domain.StateFailed {
		t.Fatalf("state = %s, want FAILED", event.State())
	}
	if len(h.store.outcomes) != 0 {
		t.Fatal("no HTTP call happened, so there is no attempt to record")
	}
	if event.LastError() == nil {
		t.Fatal("the reason must be visible to whoever investigates later")
	}
}

// A subscription service outage must not consume the event.
func TestResolverFailureHandsEventToRetry(t *testing.T) {
	h := newHarness()
	h.resolver.err = errBoom
	event := testEvent(domain.StatePending)

	if err := h.uc.Execute(context.Background(), event, domain.SourceSystem, "corr-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.State() != domain.StateRetrying {
		t.Fatalf("state = %s, want RETRYING", event.State())
	}
	if h.sender.calls != 0 {
		t.Fatal("the webhook must not be called when the subscription is unknown")
	}
}

func TestSkipsEventsThatAreNotDispatchable(t *testing.T) {
	for _, state := range []domain.EventState{
		domain.StateDelivered, domain.StateDelivering, domain.StateFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			h := newHarness()
			event := testEvent(state)

			if err := h.uc.Execute(context.Background(), event, domain.SourceSystem, "corr-1"); err != nil {
				t.Fatalf("a redelivered message must not be an error: %v", err)
			}
			if h.store.claims != 0 || h.sender.calls != 0 {
				t.Fatal("nothing should happen for a non-dispatchable event")
			}
		})
	}
}

// At-least-once delivery plus several dispatcher instances means the same
// event can be picked up twice. Only the instance that wins the claim delivers.
func TestLosingTheClaimSkipsDelivery(t *testing.T) {
	h := newHarness()
	h.store.claimGranted = false
	event := testEvent(domain.StatePending)

	if err := h.uc.Execute(context.Background(), event, domain.SourceSystem, "corr-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if h.sender.calls != 0 {
		t.Fatal("the instance that lost the claim must not deliver")
	}
	if len(h.store.outcomes) != 0 {
		t.Fatal("the instance that lost the claim must not write an attempt")
	}
}

// A failed write must surface, so the consumer leaves the offset uncommitted
// and the message is redelivered instead of silently lost.
func TestPersistenceFailurePropagates(t *testing.T) {
	h := newHarness()
	h.sender.response = webhookOK(200)
	h.store.recordErr = errBoom

	err := h.uc.Execute(context.Background(), testEvent(domain.StatePending), domain.SourceSystem, "corr-1")
	if err == nil {
		t.Fatal("expected the persistence failure to propagate to the consumer")
	}
}

// Losing the monitoring stream is not a reason to fail a delivery that already
// succeeded.
func TestPublisherFailureDoesNotFailTheDelivery(t *testing.T) {
	h := newHarness()
	h.sender.response = webhookOK(200)
	h.publisher.err = errBoom
	event := testEvent(domain.StatePending)

	if err := h.uc.Execute(context.Background(), event, domain.SourceSystem, "corr-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.State() != domain.StateDelivered {
		t.Fatalf("state = %s, want DELIVERED", event.State())
	}
}

func TestTraceabilityHeadersAreSent(t *testing.T) {
	h := newHarness()
	h.sender.response = webhookOK(200)
	event := testEvent(domain.StatePending)

	if err := h.uc.Execute(context.Background(), event, domain.SourceSelfService, "corr-42"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[string]string{
		"X-Notification-Event-Id": "nev-1",
		"X-Event-Id":              "EVT001",
		"X-Event-Type":            "credit_card_payment",
		"X-Client-Id":             "CLIENT001",
		"X-Dispatch-Source":       "SELF_SERVICE",
		"X-Correlation-Id":        "corr-42",
	}
	for k, v := range want {
		if got := h.sender.lastReq.Headers[k]; got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}
