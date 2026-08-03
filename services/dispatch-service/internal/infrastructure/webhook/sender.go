package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"net/http"
	"time"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
)

// maxResponseBody caps what we read back from a client endpoint. A webhook
// that answers with a hundred megabytes should not be able to exhaust the
// dispatcher's memory.
const maxResponseBody = 64 << 10

// SenderOptions configures the HTTP delivery adapter.
type SenderOptions struct {
	Timeout     time.Duration
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// Sender delivers payloads to client webhooks over HTTP.
//
// It retries transient failures in place. Those in-process attempts are not
// persisted: they are transport noise, and writing a row for each one buries
// the audit trail in retries that mean nothing to the client. What is
// persisted is the consolidated outcome of the cycle — and the number of
// attempts it took, in the logs and metrics.
type Sender struct {
	client *http.Client
	guard  *Guard
	opts   SenderOptions
	log    *slog.Logger
	rand   *rand.Rand
}

func NewSender(guard *Guard, opts SenderOptions, log *slog.Logger) *Sender {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		// Second SSRF layer: this runs with the address the socket is about to
		// connect to, after DNS has resolved.
		Control: guard.ControlDial,
	}

	return &Sender{
		client: &http.Client{
			Timeout: opts.Timeout,
			Transport: &http.Transport{
				DialContext:         dialer.DialContext,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
			// Redirects are not followed. A 302 to an internal address is the
			// simplest way around a URL allowlist, and a webhook has no
			// legitimate reason to redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		guard: guard,
		opts:  opts,
		log:   log,
		//nolint:gosec // jitter, not a security decision
		rand: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (s *Sender) Send(ctx context.Context, req port.WebhookRequest) (port.WebhookResponse, error) {
	started := time.Now()

	// First SSRF layer, re-checked on every delivery rather than trusted from
	// subscription time: DNS and allowlists change.
	if err := s.guard.ValidateURL(ctx, req.Subscription.WebhookURL); err != nil {
		return port.WebhookResponse{Duration: time.Since(started), Attempts: 0}, err
	}

	var lastErr error

	for attempt := 1; attempt <= s.opts.MaxAttempts; attempt++ {
		resp, err := s.attempt(ctx, req)
		resp.Attempts = attempt
		resp.Duration = time.Since(started)

		switch {
		case err != nil:
			lastErr = err
			if !retryableError(err) {
				return resp, err
			}
		case retryableStatus(resp.Status):
			lastErr = fmt.Errorf("webhook returned %d", resp.Status)
		default:
			// Any other status, including 4xx, is a final answer. Retrying a
			// 400 or a 401 just means being told "no" more slowly.
			return resp, nil
		}

		if attempt == s.opts.MaxAttempts {
			return resp, err
		}

		delay := s.backoff(attempt)
		s.log.Warn("webhook attempt failed, retrying in place",
			slog.String("webhook_url", req.Subscription.WebhookURL),
			slog.Int("attempt", attempt),
			slog.Int("max_attempts", s.opts.MaxAttempts),
			slog.Int("status", resp.Status),
			slog.Duration("retry_in", delay),
			slog.Any("error", err),
		)

		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-time.After(delay):
		}
	}

	return port.WebhookResponse{Attempts: s.opts.MaxAttempts, Duration: time.Since(started)}, lastErr
}

func (s *Sender) attempt(ctx context.Context, req port.WebhookRequest) (port.WebhookResponse, error) {
	sub := req.Subscription

	method := sub.HTTPMethod
	if method == "" {
		method = http.MethodPost
	}

	body := []byte(req.Payload)
	httpReq, err := http.NewRequestWithContext(ctx, method, sub.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return port.WebhookResponse{}, fmt.Errorf("build webhook request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Cobre-Notifications/1.0")
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	if sub.HMACSecret != "" {
		now := time.Now()
		httpReq.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", now.Unix()))
		httpReq.Header.Set(HeaderSignature, Sign(sub.HMACSecret, now, body))
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return port.WebhookResponse{}, unwrapTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return port.WebhookResponse{Status: resp.StatusCode}, fmt.Errorf("read webhook response: %w", err)
	}

	return port.WebhookResponse{Status: resp.StatusCode, Body: normaliseBody(raw)}, nil
}

// backoff grows exponentially and adds full jitter, so that a webhook coming
// back after an outage is not hit by every pending delivery at the same
// instant.
func (s *Sender) backoff(attempt int) time.Duration {
	exp := float64(s.opts.BaseDelay) * math.Pow(2, float64(attempt-1))
	if max := float64(s.opts.MaxDelay); max > 0 && exp > max {
		exp = max
	}
	if exp <= 0 {
		return 0
	}
	return time.Duration(s.rand.Int63n(int64(exp)) + int64(s.opts.BaseDelay))
}

// retryableStatus: server-side problems and explicit back-pressure are worth
// another try. Client errors are not — the request itself is what is wrong.
func retryableStatus(status int) bool {
	return status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout
}

// retryableError distinguishes "the network hiccuped" from "we refused to make
// this call". An SSRF rejection must never be retried: nothing about it will
// change, and retrying only amplifies the probe.
func retryableError(err error) bool {
	switch {
	case errors.Is(err, ErrAddressBlocked),
		errors.Is(err, ErrSchemeNotAllowed),
		errors.Is(err, ErrPortNotAllowed),
		errors.Is(err, ErrHostNotResolvable):
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	default:
		return true
	}
}

// unwrapTransportError surfaces the guard's rejection instead of the generic
// url.Error wrapper the HTTP client puts around it.
func unwrapTransportError(err error) error {
	var urlErr *net.OpError
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		if errors.Is(urlErr.Err, ErrAddressBlocked) {
			return urlErr.Err
		}
	}
	if errors.Is(err, ErrAddressBlocked) {
		return err
	}
	return fmt.Errorf("webhook request failed: %w", err)
}

// normaliseBody keeps the response queryable in a JSONB column. A webhook that
// answers with plain text still has to be storable, so non-JSON bodies are
// wrapped rather than dropped.
func normaliseBody(raw []byte) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if json.Valid(trimmed) {
		return json.RawMessage(trimmed)
	}
	wrapped, err := json.Marshal(map[string]string{"raw": string(trimmed)})
	if err != nil {
		return nil
	}
	return wrapped
}

var _ port.WebhookSender = (*Sender)(nil)
