package dnscache

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const DefaultTTL = 2 * time.Minute
const defaultMaxEntries = 512

type resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

type Options struct {
	TTL         time.Duration
	MaxEntries  int
	Resolver    resolver
	DialContext DialContextFunc
	Now         func() time.Time
}

type Dialer struct {
	ttl         time.Duration
	maxEntries  int
	resolver    resolver
	dialContext DialContextFunc
	now         func() time.Time

	mu      sync.RWMutex
	cache   map[string]cacheEntry
	flights singleflight.Group
}

type cacheEntry struct {
	ips       []net.IPAddr
	expiresAt time.Time
}

func NewDialer(ttl time.Duration) *Dialer {
	return NewDialerWithOptions(Options{TTL: ttl})
}

func NewDialerWithOptions(opts Options) *Dialer {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}

	res := opts.Resolver
	if res == nil {
		res = net.DefaultResolver
	}

	dialContext := opts.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		dialContext = dialer.DialContext
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Dialer{
		ttl:         ttl,
		maxEntries:  maxEntries,
		resolver:    res,
		dialContext: dialContext,
		now:         now,
		cache:       make(map[string]cacheEntry),
	}
}

func ConfigureTransport(transport *http.Transport, ttl time.Duration) {
	dialer := NewDialer(ttl)
	transport.DialContext = dialer.DialContext
}

func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" || net.ParseIP(host) != nil || network == "unix" {
		return d.dialContext(ctx, network, address)
	}

	ips, err := d.lookup(ctx, strings.ToLower(host))
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, ipAddr := range ips {
		if !matchesNetwork(network, ipAddr.IP) {
			continue
		}
		target := net.JoinHostPort(ipAddr.IP.String(), port)
		conn, err := d.dialContext(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("dns cache: no %s addresses for %s", network, host)
}

func (d *Dialer) lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	now := d.now()

	d.mu.RLock()
	entry, ok := d.cache[host]
	d.mu.RUnlock()
	if ok && now.Before(entry.expiresAt) && len(entry.ips) > 0 {
		return cloneIPs(entry.ips), nil
	}

	result, err, _ := d.flights.Do(host, func() (interface{}, error) {
		d.mu.RLock()
		entry, ok := d.cache[host]
		d.mu.RUnlock()
		if ok && d.now().Before(entry.expiresAt) && len(entry.ips) > 0 {
			return cloneIPs(entry.ips), nil
		}

		ips, err := d.resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("dns cache: no addresses for %s", host)
		}

		ips = cloneIPs(ips)
		d.mu.Lock()
		d.pruneLocked(d.now())
		d.cache[host] = cacheEntry{ips: ips, expiresAt: d.now().Add(d.ttl)}
		d.enforceMaxEntriesLocked(host)
		d.mu.Unlock()

		return cloneIPs(ips), nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]net.IPAddr), nil
}

func (d *Dialer) pruneLocked(now time.Time) {
	for host, entry := range d.cache {
		if !now.Before(entry.expiresAt) {
			delete(d.cache, host)
		}
	}
}

func (d *Dialer) enforceMaxEntriesLocked(protectedHost string) {
	for len(d.cache) > d.maxEntries {
		for host := range d.cache {
			if host == protectedHost && len(d.cache) > 1 {
				continue
			}
			delete(d.cache, host)
			break
		}
	}
}

func matchesNetwork(network string, ip net.IP) bool {
	switch {
	case strings.HasSuffix(network, "4"):
		return ip.To4() != nil
	case strings.HasSuffix(network, "6"):
		return ip.To4() == nil
	default:
		return true
	}
}

func cloneIPs(ips []net.IPAddr) []net.IPAddr {
	cloned := make([]net.IPAddr, len(ips))
	copy(cloned, ips)
	return cloned
}
