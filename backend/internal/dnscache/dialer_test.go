package dnscache

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeResolver struct {
	mu    sync.Mutex
	calls int
	ips   []net.IPAddr
}

func (r *fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	out := make([]net.IPAddr, len(r.ips))
	copy(out, r.ips)
	return out, nil
}

func (r *fakeResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestDialContextCachesHostnameLookups(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver := &fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}}
	dialed := make([]string, 0, 2)

	dialer := NewDialerWithOptions(Options{
		TTL:      time.Minute,
		Resolver: resolver,
		Now:      func() time.Time { return now },
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			c1, c2 := net.Pipe()
			c2.Close()
			return c1, nil
		},
	})

	for i := 0; i < 2; i++ {
		conn, err := dialer.DialContext(context.Background(), "tcp", "example.test:443")
		if err != nil {
			t.Fatalf("DialContext #%d: %v", i+1, err)
		}
		conn.Close()
	}

	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.callCount())
	}
	if len(dialed) != 2 || dialed[0] != "203.0.113.10:443" || dialed[1] != "203.0.113.10:443" {
		t.Fatalf("dialed addresses = %#v", dialed)
	}
}

func TestDialContextRefreshesAfterTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver := &fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}}

	dialer := NewDialerWithOptions(Options{
		TTL:      time.Minute,
		Resolver: resolver,
		Now:      func() time.Time { return now },
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			c1, c2 := net.Pipe()
			c2.Close()
			return c1, nil
		},
	})

	conn, err := dialer.DialContext(context.Background(), "tcp", "example.test:443")
	if err != nil {
		t.Fatalf("first DialContext: %v", err)
	}
	conn.Close()

	now = now.Add(time.Minute + time.Nanosecond)
	conn, err = dialer.DialContext(context.Background(), "tcp", "example.test:443")
	if err != nil {
		t.Fatalf("second DialContext: %v", err)
	}
	conn.Close()

	if resolver.callCount() != 2 {
		t.Fatalf("resolver calls = %d, want 2", resolver.callCount())
	}
}

func TestDialContextBypassesResolverForIPLiteral(t *testing.T) {
	resolver := &fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}}
	var dialed string

	dialer := NewDialerWithOptions(Options{
		Resolver: resolver,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = address
			c1, c2 := net.Pipe()
			c2.Close()
			return c1, nil
		},
	})

	conn, err := dialer.DialContext(context.Background(), "tcp", "198.51.100.25:443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()

	if resolver.callCount() != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolver.callCount())
	}
	if dialed != "198.51.100.25:443" {
		t.Fatalf("dialed = %q, want IP literal", dialed)
	}
}

func TestDialContextFiltersByNetworkFamily(t *testing.T) {
	resolver := &fakeResolver{ips: []net.IPAddr{
		{IP: net.ParseIP("2001:db8::1")},
		{IP: net.ParseIP("203.0.113.10")},
	}}
	var dialed string

	dialer := NewDialerWithOptions(Options{
		Resolver: resolver,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = address
			c1, c2 := net.Pipe()
			c2.Close()
			return c1, nil
		},
	})

	conn, err := dialer.DialContext(context.Background(), "tcp4", "example.test:443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()

	if dialed != "203.0.113.10:443" {
		t.Fatalf("dialed = %q, want IPv4 address", dialed)
	}
}
