package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"syscall"
)

// Webhook URLs are supplied by clients. That makes every outbound delivery a
// potential Server-Side Request Forgery: a client can point a subscription at
// 169.254.169.254 and ask the platform to fetch cloud credentials on their
// behalf, or at an internal address to probe the private network.
//
// OWASP A10. The guard below is the mitigation, and it works in two layers:
//
//  1. ValidateURL rejects the obvious cases when the subscription is created
//     and again before every call: wrong scheme, odd port, a hostname that
//     resolves into a range we will never talk to.
//
//  2. ControlDial re-checks the address the socket is actually about to
//     connect to. This is the layer that matters: a hostname can resolve to a
//     public address during validation and to 127.0.0.1 a second later — DNS
//     rebinding. Validating the URL alone does not stop that; checking at dial
//     time does.
var (
	ErrSchemeNotAllowed  = errors.New("webhook scheme is not allowed")
	ErrPortNotAllowed    = errors.New("webhook port is not allowed")
	ErrHostNotResolvable = errors.New("webhook host cannot be resolved")
	ErrAddressBlocked    = errors.New("webhook address is in a blocked range")
)

// cgnat is RFC 6598 carrier-grade NAT space, which net.IP.IsPrivate does not
// report as private but which is never a legitimate webhook destination.
var cgnat = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// Guard validates webhook destinations.
type Guard struct {
	// RequireHTTPS is true in any real environment. Local demos run the client
	// webhook over plain HTTP on localhost, and turning this off is an explicit,
	// visible decision rather than a quietly weakened validator.
	RequireHTTPS bool

	// AllowPrivateNetworks exists for the same reason and carries the same
	// warning.
	AllowPrivateNetworks bool

	// Resolver is injectable so the rules can be tested without real DNS.
	Resolver func(ctx context.Context, host string) ([]net.IP, error)
}

func NewGuard(requireHTTPS, allowPrivate bool) *Guard {
	return &Guard{
		RequireHTTPS:         requireHTTPS,
		AllowPrivateNetworks: allowPrivate,
		Resolver:             defaultResolver,
	}
}

func defaultResolver(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// ValidateURL checks a webhook destination before any connection is opened.
func (g *Guard) ValidateURL(ctx context.Context, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid webhook url: %w", err)
	}

	switch parsed.Scheme {
	case "https":
	case "http":
		if g.RequireHTTPS {
			return fmt.Errorf("%w: %q, only https is accepted", ErrSchemeNotAllowed, parsed.Scheme)
		}
	default:
		return fmt.Errorf("%w: %q", ErrSchemeNotAllowed, parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrHostNotResolvable)
	}

	if err := g.checkPort(parsed.Port(), parsed.Scheme); err != nil {
		return err
	}

	// A literal IP needs no lookup.
	if ip := net.ParseIP(host); ip != nil {
		return g.checkIP(ip)
	}

	ips, err := g.Resolver(ctx, host)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrHostNotResolvable, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: %s resolved to nothing", ErrHostNotResolvable, host)
	}

	// Every resolved address must be acceptable. A single blocked answer is
	// enough to reject the host: which one the dialer would pick is not ours
	// to predict.
	for _, ip := range ips {
		if err := g.checkIP(ip); err != nil {
			return err
		}
	}
	return nil
}

// deniedPorts are well-known service ports that no webhook has any business
// living on. Reaching them is port scanning, not delivery.
var deniedPorts = map[int]string{
	22: "ssh", 23: "telnet", 25: "smtp", 111: "rpcbind", 135: "msrpc",
	139: "netbios", 445: "smb", 1433: "mssql", 1521: "oracle",
	2049: "nfs", 2375: "docker", 2379: "etcd", 3306: "mysql",
	3389: "rdp", 5432: "postgres", 5672: "amqp", 6379: "redis",
	9042: "cassandra", 9092: "kafka", 9200: "elasticsearch",
	11211: "memcached", 27017: "mongodb",
}

// checkPort applies a denylist rather than an allowlist.
//
// An allowlist of 443 sounds stricter, but it breaks legitimate clients who
// terminate TLS on a non-standard port, and it buys little: the real defence
// against internal probing is refusing private addresses, which happens in
// checkIP. What a port rule adds is closing the remaining gap — a public host
// that happens to also expose a datastore — and blocking privileged ports that
// are never web endpoints.
func (g *Guard) checkPort(rawPort, scheme string) error {
	if rawPort == "" {
		return nil // implicit 80/443
	}

	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%w: %q is not a valid port", ErrPortNotAllowed, rawPort)
	}

	if service, denied := deniedPorts[port]; denied {
		return fmt.Errorf("%w: %d is %s, not a webhook endpoint", ErrPortNotAllowed, port, service)
	}

	// Privileged ports other than the web ones are system services.
	if port < 1024 && port != 80 && port != 443 {
		return fmt.Errorf("%w: %d is a privileged port (scheme %s)", ErrPortNotAllowed, port, scheme)
	}

	return nil
}

func (g *Guard) checkIP(ip net.IP) error {
	if g.AllowPrivateNetworks {
		return nil
	}

	switch {
	case ip.IsLoopback():
		return fmt.Errorf("%w: %s is loopback", ErrAddressBlocked, ip)
	case ip.IsPrivate():
		return fmt.Errorf("%w: %s is a private address", ErrAddressBlocked, ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		// 169.254.169.254, the cloud instance metadata service, lives here.
		return fmt.Errorf("%w: %s is link-local", ErrAddressBlocked, ip)
	case ip.IsUnspecified():
		return fmt.Errorf("%w: %s is unspecified", ErrAddressBlocked, ip)
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("%w: %s is multicast", ErrAddressBlocked, ip)
	case ip.Equal(net.IPv4bcast):
		return fmt.Errorf("%w: %s is broadcast", ErrAddressBlocked, ip)
	case cgnat.Contains(ip.To4()):
		return fmt.Errorf("%w: %s is carrier-grade NAT space", ErrAddressBlocked, ip)
	}
	return nil
}

// ControlDial is installed on the HTTP transport's dialer. It runs after DNS
// resolution, with the address the socket is about to connect to, which closes
// the DNS-rebinding window that URL validation alone leaves open.
func (g *Guard) ControlDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: cannot parse %q", ErrAddressBlocked, address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q is not an IP", ErrAddressBlocked, host)
	}
	return g.checkIP(ip)
}
