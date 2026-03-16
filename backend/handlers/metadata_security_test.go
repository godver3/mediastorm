package handlers

import (
	"testing"
)

func TestIsYouTubeURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		// Valid YouTube URLs
		{"youtube.com", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", true},
		{"youtube.com no www", "https://youtube.com/watch?v=abc", true},
		{"m.youtube.com", "https://m.youtube.com/watch?v=abc", true},
		{"youtu.be", "https://youtu.be/dQw4w9WgXcQ", true},
		{"http youtube", "http://youtube.com/watch?v=abc", true},

		// Invalid / SSRF attempts
		{"attacker with youtube in path", "https://attacker.com/youtube.com/watch?v=abc", false},
		{"subdomain spoof", "https://youtube.com.evil.com/watch?v=abc", false},
		{"attacker with youtu.be in path", "https://evil.com/youtu.be/abc", false},
		{"localhost", "http://127.0.0.1/youtube.com", false},
		{"empty", "", false},
		{"not a url", "not-a-url", false},
		{"no host", "file:///youtube.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isYouTubeURL(tt.url)
			if got != tt.want {
				t.Errorf("isYouTubeURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
