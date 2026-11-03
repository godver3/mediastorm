package jellyfinbridge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

// Android TV plays a version picked on the details page like Jellyfin's own
// versions: it loads the version as an item, queues the series from that
// item, asks PlaybackInfo for it and direct plays the source whose ID is the
// item ID, without a token. Reports carry the version ID as item.
func TestAndroidTVPlaysPickedVersion(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 3})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"title": "Show.S01E01.1080p.WEB.H264-ONE", "guid": "g1"},
			{"title": "Show.S01E01.1080p.WEB.H264-TWO", "guid": "g2"},
			{"title": "Show.S01E01.1080p.WEB.H264-THREE", "guid": "g3"},
		})
	}
	var resolved map[string]any
	h.srv.deps.Handlers.PlaybackResolve = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &resolved)
		writeJSON(w, http.StatusOK, map[string]any{"queueId": 0, "webdavPath": "/webdav/two.mkv"})
	}
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"durationSeconds": 1440,
			"videoStreams":    []map[string]any{{"index": 0, "codecName": "h264"}},
			"audioStreams":    []map[string]any{{"index": 1, "codecName": "eac3", "language": "ger"}, {"index": 2, "codecName": "aac", "language": "jpn"}},
		})
	}
	tok := h.login("Living Room", "alice-pass")
	atv := func(method, path, token string, body any) *httptest.ResponseRecorder {
		return h.doWithClient(method, path, token, "Jellyfin Android TV", body)
	}

	next := decode[dto.QueryResult[dto.BaseItemDto]](t, atv(http.MethodGet, "/Shows/NextUp", tok, nil))
	seriesID, episode := next.Items[0].SeriesID, next.Items[0].ID
	details := versionsAfterPlay(t, episode, func(method, path string, body any) *httptest.ResponseRecorder {
		return atv(method, path, tok, body)
	}, func() int { return 0 })
	if len(details.MediaSources) != 4 {
		t.Fatalf("versions = %d, want auto + 3", len(details.MediaSources))
	}
	version := details.MediaSources[2].ID // #2 = TWO
	hasSource := func(item dto.BaseItemDto, id string) bool {
		for _, ms := range item.MediaSources {
			if ms.ID == id {
				return true
			}
		}
		return false
	}

	// The picked version is an item of its own.
	v := decode[dto.BaseItemDto](t, atv(http.MethodGet, "/Items/"+version, tok, nil))
	if v.ID != version || v.Type != dto.KindEpisode || v.SeriesID != seriesID || !hasSource(v, version) || len(v.MediaSources) != 4 {
		t.Fatalf("version item = %s %s with %d sources", v.ID, v.Type, len(v.MediaSources))
	}
	// Playing queues the series from that item.
	queue := decode[dto.QueryResult[dto.BaseItemDto]](t, atv(http.MethodGet, "/Shows/"+seriesID+"/Episodes?StartItemId="+version+"&IsMissing=false", tok, nil))
	if len(queue.Items) == 0 || queue.Items[0].ID != version || !hasSource(queue.Items[0], version) {
		t.Fatalf("queue starts with %+v", queue.Items)
	}

	info := decode[dto.PlaybackInfoResponse](t, atv(http.MethodPost, "/Items/"+version+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": version, "AudioStreamIndex": 2}))
	if len(info.MediaSources) != 1 || info.MediaSources[0].ID != version {
		t.Fatalf("playback info = %+v", info)
	}
	src := info.MediaSources[0]
	if res, _ := resolved["result"].(map[string]any); res == nil || res["guid"] != "g2" {
		t.Fatalf("resolved %v, want the picked release", resolved)
	}
	if !src.SupportsDirectPlay || src.DefaultAudioStreamIndex == nil || *src.DefaultAudioStreamIndex != 2 {
		t.Fatalf("source direct play %v audio %v", src.SupportsDirectPlay, src.DefaultAudioStreamIndex)
	}

	// Direct play: the item path is the version, no token.
	q := url.Values{"static": {"true"}, "mediaSourceId": {src.ID}, "tag": {src.ETag}}
	if rec := atv(http.MethodGet, "/Videos/"+version+"/stream.mkv?"+q.Encode(), "", nil); rec.Code >= 300 {
		t.Fatalf("direct play = %d %s", rec.Code, rec.Body.String())
	}
	// Transcoding uses the source's TranscodingUrl (redirected to the playlist).
	if rec := atv(http.MethodGet, src.TranscodingURL, "", nil); rec.Code >= 400 {
		t.Fatalf("transcoding url = %d %s", rec.Code, rec.Body.String())
	}
	if rec := atv(http.MethodPost, "/Sessions/Playing/Progress", tok, map[string]any{"ItemId": version, "MediaSourceId": version, "PlaySessionId": info.PlaySessionID, "PositionTicks": 600_000_000}); rec.Code != http.StatusNoContent {
		t.Fatalf("progress = %d", rec.Code)
	}
}

// Fire TV's audio output fails on FLAC from Android TV's direct play (and the
// app's retry crashes), so files with a FLAC track go through HLS there.
func TestAndroidTVAvoidsDirectPlayWithFLAC(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	h.srv.deps.Handlers.PrequeueStatus = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.PrequeueStatusResponse{
			PrequeueID: "pq1", Status: playback.PrequeueStatusReady, StreamPath: "/webdav/movie.mkv",
			Duration: 120, SelectedAudioTrack: 1, SelectedSubtitleTrack: -1,
			AudioTracks: []playback.AudioTrackInfo{{Index: 1, Codec: "eac3", Language: "ger"}, {Index: 2, Codec: "flac", Language: "jpn"}},
		})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	for client, want := range map[string]bool{"Jellyfin Android TV": false, "Findroid": true} {
		info := decode[dto.PlaybackInfoResponse](t, h.doWithClient(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, client, map[string]any{"MediaSourceId": item}))
		if len(info.MediaSources) != 1 || info.MediaSources[0].SupportsDirectPlay != want || info.MediaSources[0].TranscodingURL == "" {
			t.Fatalf("%s: sources = %+v, want direct play %v", client, info.MediaSources, want)
		}
	}
}

// On an audio track switch Android TV reports the old playback stopped while
// it asks PlaybackInfo again, which hands out the same play session. The
// stop must not end the session the app is about to play.
func TestAndroidTVTrackSwitchKeepsSession(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h)
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	atv := func(method, path string, body any) *httptest.ResponseRecorder {
		return h.doWithClient(method, path, tok, "Jellyfin Android TV", body)
	}
	first := decode[dto.PlaybackInfoResponse](t, atv(http.MethodPost, "/Items/"+item+"/PlaybackInfo", map[string]any{"MediaSourceId": item}))
	again := decode[dto.PlaybackInfoResponse](t, atv(http.MethodPost, "/Items/"+item+"/PlaybackInfo", map[string]any{"MediaSourceId": item, "AudioStreamIndex": 1}))
	if again.PlaySessionID != first.PlaySessionID {
		t.Fatalf("session not reused: %s vs %s", again.PlaySessionID, first.PlaySessionID)
	}
	atv(http.MethodPost, "/Sessions/Playing/Stopped", map[string]any{"ItemId": item, "PlaySessionId": first.PlaySessionID, "PositionTicks": 10_000_000})
	if _, ok := h.srv.plays.get(first.PlaySessionID); !ok {
		t.Fatal("stop report of the old playback ended the reissued session")
	}

	// A plain stop ends the session.
	h2 := newHarness(t, config.JellyfinBridgeSettings{})
	installFakePlayback(t, h2)
	tok2 := h2.login("Living Room", "alice-pass")
	item2 := movieID(t, h2, tok2)
	info := decode[dto.PlaybackInfoResponse](t, h2.doWithClient(http.MethodPost, "/Items/"+item2+"/PlaybackInfo", tok2, "Jellyfin Android TV", map[string]any{}))
	h2.doWithClient(http.MethodPost, "/Sessions/Playing/Stopped", tok2, "Jellyfin Android TV", map[string]any{"ItemId": item2, "PlaySessionId": info.PlaySessionID, "PositionTicks": 10_000_000})
	if _, ok := h2.srv.plays.get(info.PlaySessionID); ok {
		t.Fatal("stopped session still open")
	}
}
