package subscriptions_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/subscriptions"
)

func TestResolveReturnsSubscription(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"subscription_id": "sub-1",
			"client_id": "CLIENT001",
			"event_type": "credit_card_payment",
			"webhook_url": "https://client.example.com/hooks",
			"http_method": "POST",
			"expected_status": 201,
			"hmac_secret": "s3cret",
			"status": "ACTIVE"
		}`))
	}))
	defer srv.Close()

	sub, err := subscriptions.NewResolver(srv.URL, time.Second).
		Resolve(context.Background(), "CLIENT001", "credit_card_payment")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub == nil {
		t.Fatal("expected a subscription")
	}
	if sub.ExpectedStatus != 201 || sub.HMACSecret != "s3cret" {
		t.Fatalf("subscription not mapped correctly: %+v", sub)
	}
	if gotQuery != "client_id=CLIENT001&event_type=credit_card_payment" {
		t.Fatalf("query = %q — the lookup must be keyed on both client and event type", gotQuery)
	}
}

// Not subscribed is a normal answer, not an outage.
func TestResolveTreatsNotFoundAsNoSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	sub, err := subscriptions.NewResolver(srv.URL, time.Second).
		Resolve(context.Background(), "CLIENT001", "credit_card_payment")
	if err != nil {
		t.Fatalf("404 must not be an error: %v", err)
	}
	if sub != nil {
		t.Fatal("expected no subscription")
	}
}

// An outage must be distinguishable from "not subscribed", because the two
// lead to opposite decisions: retry versus close the event.
func TestResolveSurfacesServiceErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := subscriptions.NewResolver(srv.URL, time.Second).
		Resolve(context.Background(), "CLIENT001", "credit_card_payment")
	if err == nil {
		t.Fatal("a 500 from the subscription service must surface as an error")
	}
}

// Tenant isolation is mandatory, so a mismatched answer is refused rather than
// delivered on.
func TestResolveRejectsAnswerForAnotherClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"subscription_id": "sub-9",
			"client_id": "SOMEONE_ELSE",
			"event_type": "credit_card_payment",
			"webhook_url": "https://attacker.example.com/hooks",
			"http_method": "POST",
			"expected_status": 200
		}`))
	}))
	defer srv.Close()

	sub, err := subscriptions.NewResolver(srv.URL, time.Second).
		Resolve(context.Background(), "CLIENT001", "credit_card_payment")
	if err == nil {
		t.Fatal("a subscription belonging to another client must be refused")
	}
	if sub != nil {
		t.Fatal("no subscription may be returned on mismatch")
	}
}
