package utils

import "testing"

func TestValidateMediaURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		// Allowed
		{"", false},
		{"pipe:0", false},
		{"http://example.com/video.mp4", false},
		{"https://cdn.example.com/stream.ts", false},
		{"HTTP://EXAMPLE.COM/FILE", false},

		// Blocked
		{"file:///etc/passwd", true},
		{"ftp://evil.com/payload", true},
		{"gopher://evil.com", true},
		{"data:text/plain,hello", true},
		{"smb://share/file", true},
	}

	for _, tt := range tests {
		err := ValidateMediaURL(tt.url)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateMediaURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
		}
	}
}

func TestEncodeURLWithSpaces(t *testing.T) {
	result, err := EncodeURLWithSpaces("http://example.com/path with spaces/file name.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "path%20with%20spaces") {
		t.Errorf("expected encoded spaces in path, got %q", result)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
