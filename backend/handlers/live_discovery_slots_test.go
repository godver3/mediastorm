package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fireConcurrently sends n distinct GETs through client at one host at once.
func fireConcurrently(t *testing.T, client *http.Client, base string, n int) {
	t.Helper()
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(fmt.Sprintf("%s/r%d", base, i))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()
}

func TestDiscoveryHostSlots(t *testing.T) {
	cases := []struct {
		name      string
		hostSlots int
		want      int32
	}{
		{"default", 0, discoveryDefaultHostSlots},
		{"stremio-catalogs", stremioCatalogHostSlots, stremioCatalogHostSlots},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var active, peak int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				now := atomic.AddInt32(&active, 1)
				for {
					old := atomic.LoadInt32(&peak)
					if now <= old || atomic.CompareAndSwapInt32(&peak, old, now) {
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
				atomic.AddInt32(&active, -1)
				_, _ = w.Write([]byte("{}"))
			}))
			defer srv.Close()
			h := &LiveHandler{}
			client := h.discoveryClient(srv.Client(), "", time.Minute)
			if tc.hostSlots > 0 {
				client = h.discoveryClientWide(srv.Client(), "", time.Minute, tc.hostSlots)
			}
			fireConcurrently(t, client, srv.URL, 20)
			if got := atomic.LoadInt32(&peak); got != tc.want {
				t.Fatalf("peak concurrency=%d, want %d", got, tc.want)
			}
		})
	}
}
