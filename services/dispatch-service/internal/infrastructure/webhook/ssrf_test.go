package webhook_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/webhook"
)

func guardWithDNS(t *testing.T, answers map[string][]string) *webhook.Guard {
	t.Helper()
	g := webhook.NewGuard(true, false)
	g.Resolver = func(_ context.Context, host string) ([]net.IP, error) {
		raw, ok := answers[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		ips := make([]net.IP, 0, len(raw))
		for _, r := range raw {
			ips = append(ips, net.ParseIP(r))
		}
		return ips, nil
	}
	return g
}

// OWASP A10. These are the destinations a client must never be able to make
// the platform reach on their behalf.
func TestValidateURLBlocksSSRFTargets(t *testing.T) {
	g := guardWithDNS(t, map[string][]string{
		"client.example.com": {"93.184.216.34"},
		// The classic attack: a public-looking hostname pointing inward.
		"evil.example.com":   {"169.254.169.254"},
		"rebind.example.com": {"127.0.0.1"},
	})

	tests := []struct {
		name    string
		url     string
		wantErr error
	}{
		{"public https host", "https://client.example.com/hooks", nil},

		{"plain http", "http://client.example.com/hooks", webhook.ErrSchemeNotAllowed},
		{"file scheme", "file:///etc/passwd", webhook.ErrSchemeNotAllowed},
		{"gopher scheme", "gopher://client.example.com/", webhook.ErrSchemeNotAllowed},

		{"literal loopback", "https://127.0.0.1/hooks", webhook.ErrAddressBlocked},
		{"literal ipv6 loopback", "https://[::1]/hooks", webhook.ErrAddressBlocked},
		{"literal private", "https://10.0.0.5/hooks", webhook.ErrAddressBlocked},
		{"literal private 192.168", "https://192.168.1.10/hooks", webhook.ErrAddressBlocked},
		{"cloud metadata", "https://169.254.169.254/latest/meta-data/", webhook.ErrAddressBlocked},
		{"carrier grade nat", "https://100.100.0.1/hooks", webhook.ErrAddressBlocked},
		{"unspecified", "https://0.0.0.0/hooks", webhook.ErrAddressBlocked},

		{"hostname resolving to metadata", "https://evil.example.com/hooks", webhook.ErrAddressBlocked},
		{"hostname resolving to loopback", "https://rebind.example.com/hooks", webhook.ErrAddressBlocked},

		{"unknown host", "https://nowhere.example.com/hooks", webhook.ErrHostNotResolvable},

		// Port scanning disguised as a webhook.
		{"ssh port", "https://client.example.com:22/hooks", webhook.ErrPortNotAllowed},
		{"postgres port", "https://client.example.com:5432/hooks", webhook.ErrPortNotAllowed},
		{"redis port", "https://client.example.com:6379/hooks", webhook.ErrPortNotAllowed},
		{"kafka port", "https://client.example.com:9092/hooks", webhook.ErrPortNotAllowed},
		{"other privileged port", "https://client.example.com:161/hooks", webhook.ErrPortNotAllowed},

		// Legitimate endpoints must keep working: an allowlist of 443 alone
		// would lock out clients who terminate TLS elsewhere.
		{"explicit 443", "https://client.example.com:443/hooks", nil},
		{"high port", "https://client.example.com:8443/hooks", nil},
		{"ephemeral port", "https://client.example.com:52341/hooks", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := g.ValidateURL(context.Background(), tc.url)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected %s to be allowed, got %v", tc.url, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// The dial-time check is what actually stops DNS rebinding: the hostname was
// public when validated and resolves inward by the time the socket opens.
func TestControlDialRejectsRebinding(t *testing.T) {
	g := webhook.NewGuard(true, false)

	blocked := []string{
		"127.0.0.1:443",
		"169.254.169.254:443",
		"10.1.2.3:443",
		"[::1]:443",
	}
	for _, addr := range blocked {
		if err := g.ControlDial("tcp", addr, nil); !errors.Is(err, webhook.ErrAddressBlocked) {
			t.Errorf("ControlDial(%s) = %v, want ErrAddressBlocked", addr, err)
		}
	}

	if err := g.ControlDial("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("a public address must be dialable: %v", err)
	}
}

// The local demo runs the client webhook over HTTP on the Docker network, so
// the relaxation has to work — and has to be the only thing that enables it.
func TestPermissiveModeIsExplicit(t *testing.T) {
	const localWebhook = "http://localhost:3004/webhook"

	strict := webhook.NewGuard(true, false)
	if err := strict.ValidateURL(context.Background(), localWebhook); err == nil {
		t.Fatal("strict mode must reject plain http to a loopback host")
	}

	relaxed := webhook.NewGuard(false, true)
	if err := relaxed.ValidateURL(context.Background(), localWebhook); err != nil {
		t.Fatalf("relaxed mode should allow the local demo webhook: %v", err)
	}

	// Relaxing the guard must not also disable the dial-time check for
	// anything the operator did not opt into.
	if err := relaxed.ControlDial("tcp", "127.0.0.1:3004", nil); err != nil {
		t.Fatalf("relaxed mode must allow the loopback dial it just validated: %v", err)
	}
}
