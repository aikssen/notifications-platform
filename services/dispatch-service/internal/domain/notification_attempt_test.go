package domain_test

import (
	"testing"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

// The previous implementation resolved expected_status from the subscription
// and then ignored it, accepting any 2xx. These cases pin the corrected
// behaviour: the client's declared contract is what decides success.
func TestSubscriptionAccepts(t *testing.T) {
	tests := []struct {
		name           string
		expectedStatus int
		responseStatus int
		want           bool
	}{
		{"declared 201 accepts 201", 201, 201, true},
		{"declared 201 rejects 200", 201, 200, false},
		{"declared 201 rejects 204", 201, 204, false},
		{"declared 200 rejects 500", 200, 500, false},
		{"declared 200 rejects 404", 200, 404, false},

		// No declared status falls back to "any 2xx", which is the sensible
		// default for a subscription created without one.
		{"unset accepts 200", 0, 200, true},
		{"unset accepts 299", 0, 299, true},
		{"unset rejects 300", 0, 300, false},
		{"unset rejects 500", 0, 500, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sub := domain.Subscription{ExpectedStatus: tc.expectedStatus}
			if got := sub.Accepts(tc.responseStatus); got != tc.want {
				t.Fatalf("Accepts(%d) with expected=%d = %v, want %v",
					tc.responseStatus, tc.expectedStatus, got, tc.want)
			}
		})
	}
}
