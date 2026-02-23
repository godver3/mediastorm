package utils

import "testing"

func TestIsAllowedOrigin(t *testing.T) {
	tests := []struct {
		origin  string
		allowed bool
	}{
		// Allowed: localhost
		{"http://localhost", true},
		{"http://localhost:8081", true},
		{"https://localhost:3000", true},

		// Allowed: private IPs
		{"http://192.168.1.1", true},
		{"http://192.168.1.1:7777", true},
		{"http://10.0.0.1", true},
		{"http://10.0.0.1:8080", true},
		{"http://172.16.0.1", true},
		{"http://172.31.255.255:443", true},
		{"http://127.0.0.1", true},
		{"http://127.0.0.1:3000", true},

		// Allowed: link-local
		{"http://169.254.1.1", true},

		// Allowed: .local hostnames
		{"http://mynas.local", true},
		{"http://mynas.local:7777", true},

		// Allowed: single-label hostnames (LAN)
		{"http://mediaserver:7777", true},

		// Blocked: public domains
		{"http://example.com", false},
		{"https://evil.com", false},
		{"https://google.com", false},
		{"http://image.tmdb.org.evil.com", false},

		// Blocked: public IPs
		{"http://8.8.8.8", false},
		{"http://1.1.1.1", false},

		// Blocked: empty/invalid
		{"", false},
		{"not-a-url", false},
	}

	for _, tt := range tests {
		got := IsAllowedOrigin(tt.origin)
		if got != tt.allowed {
			t.Errorf("IsAllowedOrigin(%q) = %v, want %v", tt.origin, got, tt.allowed)
		}
	}
}
