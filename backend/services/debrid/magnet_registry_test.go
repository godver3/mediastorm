package debrid

import (
	"testing"
)

func TestMagnetRegistry_RegisterAndLookup(t *testing.T) {
	RegisterMagnet("alldebrid", "12345", "magnet:?xt=urn:btih:abc123")

	link, ok := LookupMagnet("alldebrid", "12345")
	if !ok {
		t.Fatal("expected magnet to be found")
	}
	if link != "magnet:?xt=urn:btih:abc123" {
		t.Fatalf("expected magnet link, got %s", link)
	}
}

func TestMagnetRegistry_LookupMissing(t *testing.T) {
	_, ok := LookupMagnet("alldebrid", "nonexistent")
	if ok {
		t.Fatal("expected magnet not to be found")
	}
}

func TestMagnetRegistry_DifferentProviders(t *testing.T) {
	RegisterMagnet("realdebrid", "100", "magnet:?xt=urn:btih:rd100")
	RegisterMagnet("alldebrid", "100", "magnet:?xt=urn:btih:ad100")

	rdLink, ok := LookupMagnet("realdebrid", "100")
	if !ok || rdLink != "magnet:?xt=urn:btih:rd100" {
		t.Fatalf("expected realdebrid magnet, got %q ok=%v", rdLink, ok)
	}

	adLink, ok := LookupMagnet("alldebrid", "100")
	if !ok || adLink != "magnet:?xt=urn:btih:ad100" {
		t.Fatalf("expected alldebrid magnet, got %q ok=%v", adLink, ok)
	}
}

func TestMagnetRegistry_EmptyValues(t *testing.T) {
	RegisterMagnet("alldebrid", "", "magnet:?xt=urn:btih:abc")
	RegisterMagnet("alldebrid", "999", "")

	_, ok := LookupMagnet("alldebrid", "")
	if ok {
		t.Fatal("empty torrentID should not be registered")
	}

	_, ok = LookupMagnet("alldebrid", "999")
	if ok {
		t.Fatal("empty magnet link should not be registered")
	}
}

func TestIsStaleTorrentError(t *testing.T) {
	tests := []struct {
		errMsg string
		stale  bool
	}{
		{"This magnet ID does not exists or is invalid", true},
		{"torrent not found", true},
		{"torrent not found in Torbox response", true},
		{"get torrent info failed: This magnet ID does not exists or is invalid", true},
		{"alldebrid authentication failed: invalid API key", false},
		{"connection refused", false},
		{"timeout", false},
		{"stream not found", false}, // generic not found, not torrent-specific
		{"", false},
	}

	for _, tt := range tests {
		var err error
		if tt.errMsg != "" {
			err = &testError{msg: tt.errMsg}
		}
		got := isStaleTorrentError(err)
		if got != tt.stale {
			t.Errorf("isStaleTorrentError(%q) = %v, want %v", tt.errMsg, got, tt.stale)
		}
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
