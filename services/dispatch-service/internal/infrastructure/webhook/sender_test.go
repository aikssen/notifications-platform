package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/webhook"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestSender(t *testing.T, maxAttempts int) *webhook.Sender {
	t.Helper()
	// httptest binds to loopback, so the guard has to be in permissive mode —
	// the same mode the local demo uses, and nothing more.
	return webhook.NewSender(
		webhook.NewGuard(false, true),
		webhook.SenderOptions{
			Timeout:     2 * time.Second,
			MaxAttempts: maxAttempts,
			BaseDelay:   time.Millisecond,
			MaxDelay:    5 * time.Millisecond,
		},
		quiet,
	)
}

func request(url string) port.WebhookRequest {
	return port.WebhookRequest{
		Subscription: domain.Subscription{
			WebhookURL: url,
			HTTPMethod: http.MethodPost,
			HMACSecret: "topsecret",
		},
		Payload: json.RawMessage(`{"amount":125000}`),
		Headers: map[string]string{"X-Event-Type": "payment.succeeded"},
	}
}

func TestSendSucceedsOnFirstAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"received"}`))
	}))
	defer srv.Close()

	resp, err := newTestSender(t, 3).Send(context.Background(), request(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != 200 || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d, want 200 and a single call", resp.Status, calls.Load())
	}
	if string(resp.Body) != `{"status":"received"}` {
		t.Fatalf("body = %s", resp.Body)
	}
}

// The previous implementation threw a 4xx inside its own try block, caught it,
// and retried anyway — the exact opposite of the comment above it. A client
// error is a final answer.
func TestClientErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 422} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
			}))
			defer srv.Close()

			resp, err := newTestSender(t, 3).Send(context.Background(), request(srv.URL))
			if err != nil {
				t.Fatalf("a 4xx is an answer, not a transport failure: %v", err)
			}
			if resp.Status != status {
				t.Fatalf("status = %d, want %d", resp.Status, status)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("made %d calls, want exactly 1 — 4xx must not be retried", got)
			}
		})
	}
}

func TestServerErrorsAreRetriedThenReported(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	resp, _ := newTestSender(t, 3).Send(context.Background(), request(srv.URL))

	if got := calls.Load(); got != 3 {
		t.Fatalf("made %d calls, want 3", got)
	}
	if resp.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3 reported for the logs", resp.Attempts)
	}
	if resp.Status != 500 {
		t.Fatalf("status = %d, want the last observed 500", resp.Status)
	}
}

func TestRecoversOnASubsequentAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	resp, err := newTestSender(t, 5).Send(context.Background(), request(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if resp.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", resp.Attempts)
	}
}

func TestTooManyRequestsIsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, _ = newTestSender(t, 2).Send(context.Background(), request(srv.URL))

	if got := calls.Load(); got != 2 {
		t.Fatalf("made %d calls, want 2 — 429 is back-pressure, not a client error", got)
	}
}

// A client proving the delivery is genuine is the whole point of the header.
func TestRequestIsSignedAndVerifiable(t *testing.T) {
	type captured struct {
		signature string
		timestamp string
		body      []byte
		headers   http.Header
	}
	var got captured

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = captured{
			signature: r.Header.Get(webhook.HeaderSignature),
			timestamp: r.Header.Get(webhook.HeaderTimestamp),
			body:      body,
			headers:   r.Header,
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	if _, err := newTestSender(t, 1).Send(context.Background(), request(srv.URL)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.signature == "" || got.timestamp == "" {
		t.Fatal("both the signature and its timestamp must be sent")
	}

	unix, err := strconv.ParseInt(got.timestamp, 10, 64)
	if err != nil {
		t.Fatalf("timestamp is not a unix time: %v", err)
	}

	// This is exactly what a client would run on their side.
	if !webhook.Verify("topsecret", got.signature, time.Unix(unix, 0), got.body) {
		t.Fatal("the signature does not verify against the received body")
	}
	if webhook.Verify("wrong-secret", got.signature, time.Unix(unix, 0), got.body) {
		t.Fatal("the signature verified against the wrong secret")
	}
	if webhook.Verify("topsecret", got.signature, time.Unix(unix+1, 0), got.body) {
		t.Fatal("the timestamp is not actually bound into the signature")
	}

	if got.headers.Get("X-Event-Type") != "payment.succeeded" {
		t.Fatal("traceability headers must reach the client")
	}
}

// A 302 to an internal address is the simplest way around a URL allowlist.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var redirectTarget atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTarget.Add(1)
		w.WriteHeader(200)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	resp, err := newTestSender(t, 1).Send(context.Background(), request(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != http.StatusFound {
		t.Fatalf("status = %d, want the 302 itself", resp.Status)
	}
	if redirectTarget.Load() != 0 {
		t.Fatal("the redirect target must never be contacted")
	}
}

// An SSRF rejection is a permanent decision. Retrying it only amplifies the
// probe against the internal network.
func TestBlockedDestinationIsNotRetried(t *testing.T) {
	sender := webhook.NewSender(
		webhook.NewGuard(true, false),
		webhook.SenderOptions{Timeout: time.Second, MaxAttempts: 3, BaseDelay: time.Millisecond},
		quiet,
	)

	resp, err := sender.Send(context.Background(), request("https://169.254.169.254/latest/meta-data/"))
	if err == nil {
		t.Fatal("expected the metadata endpoint to be rejected")
	}
	if resp.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 — the call must never leave the process", resp.Attempts)
	}
}

func TestNonJSONResponseIsStoredQueryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("plain text, not json"))
	}))
	defer srv.Close()

	resp, err := newTestSender(t, 1).Send(context.Background(), request(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !json.Valid(resp.Body) {
		t.Fatalf("body must be valid JSON for a JSONB column, got %q", resp.Body)
	}

	var wrapped map[string]string
	if err := json.Unmarshal(resp.Body, &wrapped); err != nil {
		t.Fatal(err)
	}
	if wrapped["raw"] != "plain text, not json" {
		t.Fatalf("wrapped body = %v", wrapped)
	}
}
