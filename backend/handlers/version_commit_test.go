package handlers

import "testing"

func TestGetBackendCommitUsesLdflagsShortDigest(t *testing.T) {
	saved := buildCommit
	t.Cleanup(func() { buildCommit = saved })

	buildCommit = "3cf66856aabbccddeeff00112233445566778899"
	if got := GetBackendCommit(); got != "3cf66856" {
		t.Fatalf("GetBackendCommit() = %q, want 3cf66856", got)
	}

	buildCommit = ""
	if got := GetBackendCommit(); got == "" {
		t.Fatal("GetBackendCommit() returned empty without ldflags")
	}
}
