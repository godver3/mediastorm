package jellyfinbridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// Active Streams stops a native player's playback: the bridge picks the stop
// up on the next progress report, tells the device to stop and refuses the
// player's further range requests until the item is started again.
func TestActiveStreamsStopsDirectPlayback(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	fp := installFakePlayback(t, h)
	var stopFor []models.PlaybackProgressUpdate
	stop := false
	h.srv.deps.StopRequested = func(profileID string, u models.PlaybackProgressUpdate) bool {
		stopFor = append(stopFor, u)
		return stop
	}
	tok := decode[dto.AuthenticationResult](t, h.doWithClient(http.MethodPost, "/Users/AuthenticateByName", "", "Android TV", map[string]string{"Username": "Living Room", "Pw": "alice-pass"})).AccessToken
	item := movieID(t, h, tok)
	info := decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, "Android TV", map[string]any{}))
	src := info.MediaSources[0]
	stream := func(query ...string) int {
		extra := ""
		if len(query) > 0 {
			extra = "&" + query[0]
		}
		req := httptest.NewRequest(http.MethodGet, "/Videos/"+item+"/stream?static=true&mediaSourceId="+src.ID+extra, nil)
		req.Header.Set("Range", "bytes=0-99")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := stream(); code != http.StatusPartialContent {
		t.Fatalf("stream = %d", code)
	}
	// The stream carries the item ID the dashboard stops by.
	if got := fp.streamQuery.Get("itemId"); got != "tmdb:movie:603" {
		t.Fatalf("stream itemId = %q, want the progress item ID", got)
	}

	client := h.srv.sockets.add(h.alice.ID, "dev-Android TV", "", nil)
	other := h.srv.sockets.add(h.alice.ID, "dev-phone", "", nil)
	report := map[string]any{"ItemId": item, "PlaySessionId": info.PlaySessionID, "PositionTicks": int64(60) * ticksPerSecond}
	h.doWithClient(http.MethodPost, "/Sessions/Playing/Progress", tok, "Android TV", report)
	if code := stream(); code != http.StatusPartialContent {
		t.Fatalf("stream before any stop = %d", code)
	}
	if len(stopFor) != 1 || stopFor[0].ItemID != "tmdb:movie:603" || stopFor[0].MediaType != "movie" {
		t.Fatalf("stop checks = %+v", stopFor)
	}

	stop = true
	h.doWithClient(http.MethodPost, "/Sessions/Playing/Progress", tok, "Android TV", report)
	select {
	case f := <-client.out:
		var msg struct {
			MessageType string
			Data        struct{ Command string }
		}
		if json.Unmarshal(f.payload, &msg) != nil || msg.MessageType != "Playstate" || msg.Data.Command != "Stop" {
			t.Fatalf("socket message = %s", f.payload)
		}
	default:
		t.Fatal("the device was not told to stop")
	}
	select {
	case f := <-other.out:
		t.Fatalf("another device of the profile was told to stop: %s", f.payload)
	default:
	}
	// The play session is gone (a token-less request finds none), and a
	// request that could open a new one is refused as well.
	if code := stream(); code == http.StatusPartialContent {
		t.Fatal("stream still served after the stop")
	}
	if code := stream("api_key=" + tok); code != http.StatusGone {
		t.Fatalf("stream with a token after stop = %d, want 410", code)
	}

	// The client reloading the cut stream right away stays stopped.
	stop = false
	info = decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, "Android TV", map[string]any{}))
	src = info.MediaSources[0]
	if code := stream("api_key=" + tok); code != http.StatusGone {
		t.Fatalf("stream after an immediate reload = %d, want 410", code)
	}

	// Starting the item again later lifts the block.
	h.srv.stopped.Range(func(k, _ any) bool {
		h.srv.stopped.Store(k, time.Now().Add(-stopReloadGrace-time.Second))
		return true
	})
	info = decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, "Android TV", map[string]any{}))
	src = info.MediaSources[0]
	if code := stream(); code != http.StatusPartialContent {
		t.Fatalf("stream after starting again = %d", code)
	}
}
