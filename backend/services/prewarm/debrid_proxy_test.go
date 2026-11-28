package prewarm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"novastream/services/debrid"
)

type proxyRefresher struct{ err error }

func (r proxyRefresher) GetDirectURL(context.Context, string) (string, error) { return "", r.err }

// A proxied debrid provider resolves and caches the link but withholds the
// direct URL; that still counts as a successful keep-alive refresh.
func TestRefreshURLsTreatsProxiedProviderAsRefreshed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		refreshed bool
	}{
		{"proxied", fmt.Errorf("wrapped: %w", debrid.ErrDirectURLProxied), true},
		{"real failure", errors.New("unrestrict failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &WarmEntry{TitleID: "t", StreamPath: "/debrid/realdebrid/abc/file/1"}
			s := &Service{entries: map[string]*WarmEntry{"k": entry}, debridStreaming: proxyRefresher{tc.err}}
			if err := s.RefreshURLs(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := !entry.LastRefresh.IsZero() && time.Since(entry.LastRefresh) < time.Minute; got != tc.refreshed {
				t.Fatalf("refreshed = %v, want %v", got, tc.refreshed)
			}
		})
	}
}
