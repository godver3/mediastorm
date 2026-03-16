package debrid

import (
	"testing"
)

func TestValidateDebridURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		// Valid debrid provider URLs
		{"real-debrid download", "https://download.real-debrid.com/d/ABC123/file.mkv", false},
		{"realdebrid alt", "https://cdn.realdebrid.com/stream/abc", false},
		{"alldebrid", "https://download.alldebrid.com/file/abc123", false},
		{"premiumize", "https://download.premiumize.me/file/abc", false},
		{"debrid-link", "https://download.debrid-link.com/stream", false},
		{"debrid-link.fr", "https://cdn.debrid-link.fr/file", false},
		{"put.io", "https://api.put.io/v2/files/123/mp4/download", false},
		{"torbox", "https://api.torbox.app/download/abc", false},
		{"offcloud", "https://offcloud.com/api/download", false},

		// Blocked URLs (SSRF attempts)
		{"localhost", "http://127.0.0.1:8080/admin", true},
		{"internal", "http://192.168.1.1/secret", true},
		{"attacker", "https://attacker.com/realdebrid.com", true},
		{"attacker subdomain", "https://real-debrid.com.evil.com/file", true},
		{"metadata endpoint", "http://169.254.169.254/latest/meta-data/", true},
		{"empty host", "file:///etc/passwd", true},
		{"no scheme", "not-a-url", true},

		// Edge cases
		{"empty", "", true},
		{"just scheme", "https://", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDebridURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDebridURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}
