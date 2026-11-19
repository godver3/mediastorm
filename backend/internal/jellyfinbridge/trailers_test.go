package jellyfinbridge

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

var fakeTrailerLookups atomic.Int32

// Trailers makes fakeMetadata a trailerSource: the primary trailer is also
// in the list (as the real service returns it), plus a duplicate and a
// link without URL.
func (fakeMetadata) Trailers(_ context.Context, q models.TrailerQuery) (*models.TrailerResponse, error) {
	fakeTrailerLookups.Add(1)
	main := models.Trailer{Name: "Official Trailer", URL: "https://www.youtube.com/watch?v=main" + fmt.Sprint(q.TMDBID)}
	return &models.TrailerResponse{PrimaryTrailer: &main, Trailers: []models.Trailer{
		{Name: "Teaser", URL: "https://www.youtube.com/watch?v=teaser"}, main, {Name: "broken"},
	}}, nil
}

// A movie's details carry its trailers for the clients' trailer button,
// looked up once and then served from the cache.
func TestDetailsCarryRemoteTrailers(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	id := movieID(t, h, tok)
	before := fakeTrailerLookups.Load()
	var item dto.BaseItemDto
	for range 3 {
		item = decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+id, tok, nil))
	}
	if n := fakeTrailerLookups.Load() - before; n != 1 {
		t.Errorf("trailer lookups = %d, want 1 (cached)", n)
	}
	if len(item.RemoteTrailers) != 2 || item.RemoteTrailers[0].Name != "Official Trailer" || item.RemoteTrailers[1].Name != "Teaser" {
		t.Fatalf("RemoteTrailers = %+v", item.RemoteTrailers)
	}
}

func TestTrailerURLsCapAndOrder(t *testing.T) {
	resp := &models.TrailerResponse{}
	for i := range 8 {
		resp.Trailers = append(resp.Trailers, models.Trailer{URL: fmt.Sprintf("https://youtu.be/%d", i)})
	}
	urls := trailerURLs(resp)
	if len(urls) != maxRemoteTrailers || urls[0].Name != "Trailer" || urls[0].URL != "https://youtu.be/0" {
		t.Fatalf("urls = %+v", urls)
	}
	if trailerURLs(nil) != nil {
		t.Fatal("nil response gave trailers")
	}
}
