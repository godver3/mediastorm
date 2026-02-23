package utils

import (
	"net"
	"net/url"
	"strings"
)

// IsAllowedOrigin checks whether an Origin header value should be trusted.
// It allows localhost, private/RFC1918 IPs, link-local IPs, .local hostnames,
// and single-label hostnames (no dots). Public internet origins are blocked.
func IsAllowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}

	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}

	hostname := parsed.Hostname()

	// Allow localhost
	if hostname == "localhost" {
		return true
	}

	// Allow .local mDNS hostnames (e.g., mybox.local)
	if strings.HasSuffix(hostname, ".local") {
		return true
	}

	// Allow single-label hostnames (no dots = LAN names)
	if !strings.Contains(hostname, ".") {
		return true
	}

	// Check if it's an IP address
	ip := net.ParseIP(hostname)
	if ip != nil {
		return isPrivateIP(ip)
	}

	return false
}

// isPrivateIP returns true for RFC1918, loopback, and link-local addresses.
func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		network *net.IPNet
	}{
		{mustParseCIDR("10.0.0.0/8")},
		{mustParseCIDR("172.16.0.0/12")},
		{mustParseCIDR("192.168.0.0/16")},
		{mustParseCIDR("127.0.0.0/8")},
		{mustParseCIDR("169.254.0.0/16")},   // link-local IPv4
		{mustParseCIDR("::1/128")},            // loopback IPv6
		{mustParseCIDR("fe80::/10")},          // link-local IPv6
		{mustParseCIDR("fc00::/7")},           // unique local IPv6
	}

	for _, r := range privateRanges {
		if r.network.Contains(ip) {
			return true
		}
	}
	return false
}

func mustParseCIDR(s string) *net.IPNet {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return network
}
