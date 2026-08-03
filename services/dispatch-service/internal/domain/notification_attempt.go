package domain

import (
	"encoding/json"
	"time"
)

// Subscription is the delivery contract a client registered: where to send a
// given event type, how, and what response counts as accepted.
type Subscription struct {
	ID             string
	ClientID       string
	EventType      string
	WebhookURL     string
	HTTPMethod     string
	ExpectedStatus int
	HMACSecret     string
}

// Accepts reports whether a response status satisfies this subscription.
//
// The client declares the status it will return on success. Treating any 2xx
// as success would silently accept a 204 from a webhook that was configured to
// answer 201 — which is exactly the kind of drift the audit trail exists to
// catch.
func (s Subscription) Accepts(status int) bool {
	if s.ExpectedStatus > 0 {
		return status == s.ExpectedStatus
	}
	return status >= 200 && status < 300
}

// NotificationAttempt is one delivery cycle and its outcome: the audit trail.
//
// Immediate in-process HTTP retries are not attempts. They are transport-level
// noise, observable in logs and metrics. One attempt is one meaningful,
// consolidated delivery cycle.
type NotificationAttempt struct {
	ID                  string
	NotificationEventID string

	// AttemptNumber is assigned by the store when the attempt is appended, so
	// that the sequence stays correct with several dispatchers running.
	AttemptNumber int

	DispatchSource DispatchSource
	Status         DeliveryStatus

	WebhookURL     string
	RequestMethod  string
	RequestPayload json.RawMessage

	ResponseStatus *int
	ResponseBody   json.RawMessage
	ErrorMessage   *string
	DurationMS     *int

	AttemptedAt time.Time
}

// AttemptOutcome is what an adapter reports back after trying to deliver.
type AttemptOutcome struct {
	ResponseStatus *int
	ResponseBody   json.RawMessage
	ErrorMessage   *string
	Duration       time.Duration
}

// NewAttempt builds the audit record for a delivery cycle.
func NewAttempt(
	id string,
	event *NotificationEvent,
	sub Subscription,
	source DispatchSource,
	status DeliveryStatus,
	outcome AttemptOutcome,
	now time.Time,
) *NotificationAttempt {
	durationMS := int(outcome.Duration.Milliseconds())

	return &NotificationAttempt{
		ID:                  id,
		NotificationEventID: event.ID,
		DispatchSource:      source,
		Status:              status,
		WebhookURL:          sub.WebhookURL,
		RequestMethod:       sub.HTTPMethod,
		RequestPayload:      event.Payload,
		ResponseStatus:      outcome.ResponseStatus,
		ResponseBody:        outcome.ResponseBody,
		ErrorMessage:        outcome.ErrorMessage,
		DurationMS:          &durationMS,
		AttemptedAt:         now,
	}
}

func (a *NotificationAttempt) Succeeded() bool {
	return a.Status == DeliverySuccess
}
