package subscriptions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

// Resolver answers the case statement's first delivery requirement:
//
//	"Confirm with the subscription if such an event has to be delivered. It's
//	 mandatory to ensure notifications sent to every client belong to events
//	 generated to that client."
//
// The lookup is keyed on (client_id, event_type) together, never on event_type
// alone. That pairing is what enforces tenant isolation at the delivery layer:
// an event can only ever be sent to a webhook its own client registered.
type Resolver struct {
	baseURL string
	client  *http.Client
}

func NewResolver(baseURL string, timeout time.Duration) *Resolver {
	return &Resolver{
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

type subscriptionResponse struct {
	SubscriptionID string `json:"subscription_id"`
	ClientID       string `json:"client_id"`
	EventType      string `json:"event_type"`
	WebhookURL     string `json:"webhook_url"`
	HTTPMethod     string `json:"http_method"`
	ExpectedStatus int    `json:"expected_status"`
	HMACSecret     string `json:"hmac_secret"`
	Status         string `json:"status"`
}

func (r *Resolver) Resolve(
	ctx context.Context,
	clientID, eventType string,
) (*domain.Subscription, error) {
	endpoint, err := url.Parse(r.baseURL + "/internal/subscriptions/resolve")
	if err != nil {
		return nil, fmt.Errorf("invalid subscriptions base url: %w", err)
	}
	query := endpoint.Query()
	query.Set("client_id", clientID)
	query.Set("event_type", eventType)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build subscription request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call subscription service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 404 is a normal answer, not a failure: this client is simply not
	// subscribed to this event type.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("subscription service returned %d: %s", resp.StatusCode, body)
	}

	var payload subscriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode subscription response: %w", err)
	}

	// Defence in depth: the caller asked about one client, so anything else
	// coming back is a bug or a compromise, and delivering on it would be a
	// cross-tenant leak.
	if payload.ClientID != clientID || payload.EventType != eventType {
		return nil, fmt.Errorf(
			"subscription service answered for %s/%s but was asked about %s/%s",
			payload.ClientID, payload.EventType, clientID, eventType)
	}

	return &domain.Subscription{
		ID:             payload.SubscriptionID,
		ClientID:       payload.ClientID,
		EventType:      payload.EventType,
		WebhookURL:     payload.WebhookURL,
		HTTPMethod:     payload.HTTPMethod,
		ExpectedStatus: payload.ExpectedStatus,
		HMACSecret:     payload.HMACSecret,
	}, nil
}

var _ port.SubscriptionResolver = (*Resolver)(nil)
