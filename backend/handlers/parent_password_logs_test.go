package handlers

import (
	"strings"
	"testing"
)

func TestLogPackageRedactsParentPasswords(t *testing.T) {
	for _, input := range []string{`X-Account-Password: fixture secret with spaces`, `{"X-Account-Password":"fixture secret with spaces"}`, `{"X-Account-Password":["fixture secret with spaces"]}`, `{"X-Account-Password":"fixture \"quoted\" secret with spaces"}`} {
		redacted := redactLogUploadContent(input)
		if strings.Contains(redacted, "fixture") || strings.Contains(redacted, "spaces") {
			t.Fatal("parent password remained in log package")
		}
	}
}
