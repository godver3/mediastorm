package utils

import (
	"testing"
)

func TestValidatePIN(t *testing.T) {
	tests := []struct {
		pin      string
		expected bool
	}{
		{"123456", true},
		{"000000", true},
		{"999999", true},
		{"12345", false},   // too short
		{"1234567", false}, // too long
		{"12345a", false},  // contains non-digit
		{"", false},        // empty
		{"abc123", false},  // contains letters
	}

	for _, test := range tests {
		result := ValidatePIN(test.pin)
		if result != test.expected {
			t.Errorf("ValidatePIN(%q) = %v, expected %v", test.pin, result, test.expected)
		}
	}
}
