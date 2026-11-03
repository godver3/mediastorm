package jellyfinbridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

func TestReleaseVersionsFollowRankingAndPlay(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 15})
	installFakePlayback(t, h)
	searches := 0
	h.srv.deps.Handlers.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
		searches++
		if r.URL.Query().Get("mediaType") != "movie" || r.URL.Query().Get("q") == "" {
			t.Errorf("search query = %v", r.URL.Query())
		}
		results := []map[string]any{}
		for i := 1; i <= 20; i++ {
			res := map[string]any{"title": fmt.Sprintf("Movie.2024.1080p.WEB-DL.H264-GRP%02d", i), "guid": fmt.Sprintf("g%d", i), "sizeBytes": int64(i) << 30, "serviceType": "usenet", "indexer": "nzbgeek"}
			if i == 2 {
				res["filterStatus"] = "filtered"
			}
			results = append(results, res)
		}
		writeJSON(w, http.StatusOK, results)
	}
	var resolved map[string]any
	h.srv.deps.Handlers.PlaybackResolve = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &resolved)
		writeJSON(w, http.StatusOK, map[string]any{"queueId": 7})
	}
	h.srv.deps.Handlers.PlaybackQueue = func(w http.ResponseWriter, r *http.Request) {
		if pathVar(r, "queueID") != "7" {
			t.Errorf("queue id = %q", pathVar(r, "queueID"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"queueId": 7, "webdavPath": "/webdav/release04.mkv", "fileSize": 4 << 30})
	}
	h.srv.deps.Handlers.ProbeVideo = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"durationSeconds": 5400,
			"videoStreams":    []map[string]any{{"index": 0, "codecName": "hevc", "colorTransfer": "smpte2084"}},
			"audioStreams":    []map[string]any{{"index": 1, "codecName": "eac3", "language": "ger"}},
			"subtitleStreams": []map[string]any{{"index": 2, "codecName": "subrip", "language": "eng"}},
		})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)

	got := versionsAfterPlay(t, item, func(method, path string, body any) *httptest.ResponseRecorder {
		return h.do(method, path, tok, body)
	}, func() int { return searches })
	if len(got.MediaSources) != 1+15 {
		t.Fatalf("versions = %d, want auto + 15", len(got.MediaSources))
	}
	if searches != 1 {
		t.Fatalf("searches = %d, want 1 (cached)", searches)
	}
	if !strings.HasPrefix(got.MediaSources[0].Name, "Auto") || got.MediaSources[0].ID != item {
		t.Fatalf("first version = %q %s, want Auto with the item ID", got.MediaSources[0].Name, got.MediaSources[0].ID)
	}
	// jellyfin-web loads the selected version as an item of its own.
	if v := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+got.MediaSources[3].ID, tok, nil)); v.ID != got.MediaSources[3].ID || len(v.MediaSources) != len(got.MediaSources) {
		t.Fatalf("version item = %s with %d versions", v.ID, len(v.MediaSources))
	}
	// Ranking order, filtered results skipped.
	if !strings.HasPrefix(got.MediaSources[1].Name, "#1 1080p H.264 · WEB-DL · GRP01 · 1.0 GB") ||
		!strings.HasPrefix(got.MediaSources[2].Name, "#2 1080p H.264 · WEB-DL · GRP03") ||
		!strings.HasPrefix(got.MediaSources[15].Name, "#15 1080p H.264 · WEB-DL · GRP16 · 16.0 GB") {
		t.Fatalf("version names = %q, %q, %q", got.MediaSources[1].Name, got.MediaSources[2].Name, got.MediaSources[15].Name)
	}
	if len(got.Trickplay) > 0 && got.Trickplay[got.MediaSources[3].ID] == nil {
		t.Fatal("trickplay not keyed by release version")
	}

	pick := got.MediaSources[3] // #3 = Release04
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+pick.ID+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": pick.ID}))
	if len(info.MediaSources) != 1 {
		t.Fatalf("playback info = %+v", info)
	}
	src := info.MediaSources[0]
	if src.ID != pick.ID || src.Name != "Movie.2024.1080p.WEB-DL.H264-GRP04" {
		t.Fatalf("source = %s %q, want %s Release04", src.ID, src.Name, pick.ID)
	}
	if res, _ := resolved["result"].(map[string]any); res == nil || res["guid"] != "g4" {
		t.Fatalf("resolved %v", resolved)
	}
	if src.RunTimeTicks == nil || *src.RunTimeTicks != 5400*10_000_000 || !strings.Contains(src.TranscodingURL, "MediaSourceId="+pick.ID) {
		t.Fatalf("source runtime %v url %s", src.RunTimeTicks, src.TranscodingURL)
	}
	hasSub := false
	for _, st := range src.MediaStreams {
		if st.Type == "Video" && st.VideoRangeType != "HDR10" {
			t.Fatalf("video range = %q", st.VideoRangeType)
		}
		hasSub = hasSub || (st.Type == "Subtitle" && st.Index == 2)
	}
	if !hasSub {
		t.Fatalf("streams = %+v", src.MediaStreams)
	}

	// The automatic version is a separate play session (prequeue).
	auto := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": got.MediaSources[0].ID}))
	if len(auto.MediaSources) != 1 || auto.MediaSources[0].ID == pick.ID || auto.PlaySessionID == info.PlaySessionID {
		t.Fatalf("auto playback info = %+v", auto)
	}
}

// Release Versions sets how many releases are offered once the item has
// been played; 0 offers none and does not search for them.
// versionsAfterPlay checks that a details request lists no versions and
// searches nothing, plays the item (which starts the release search) and
// returns the item once its versions are listed.
func versionsAfterPlay(t *testing.T, item string, do func(method, path string, body any) *httptest.ResponseRecorder, searches func() int) dto.BaseItemDto {
	t.Helper()
	before := decode[dto.BaseItemDto](t, do(http.MethodGet, "/Items/"+item, nil))
	time.Sleep(50 * time.Millisecond)
	if len(before.MediaSources) != 2 || before.MediaSources[1].Name != findReleasesLabel || searches() != 0 {
		t.Fatalf("details before playing: versions %v, %d searches; want Auto and Find releases, no search", sourceNames(before), searches())
	}
	do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", map[string]any{"MediaSourceId": item})
	var got dto.BaseItemDto
	for deadline := time.Now().Add(5 * time.Second); ; {
		got = decode[dto.BaseItemDto](t, do(http.MethodGet, "/Items/"+item, nil))
		if (len(got.MediaSources) > 1 && got.MediaSources[1].Name != findReleasesLabel) || time.Now().After(deadline) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sourceNames(item dto.BaseItemDto) []string {
	names := make([]string, len(item.MediaSources))
	for i, ms := range item.MediaSources {
		names[i] = ms.Name
	}
	return names
}

// "Find releases" is the version that asks for releases: loading it as an
// item (Android TV does on selection) starts the search without playing, and
// playing it (jellyfin-web) plays the automatic version. The releases are
// listed when the item is opened again, and the entry is gone then.
func TestFindReleasesVersion(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 3})
	installFakePlayback(t, h)
	var searches atomic.Int32
	h.srv.deps.Handlers.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
		searches.Add(1)
		writeJSON(w, http.StatusOK, []map[string]any{
			{"title": "Movie.2024.1080p.WEB.H264-ONE", "guid": "g1"},
			{"title": "Movie.2024.2160p.WEB.H265-TWO", "guid": "g2"},
		})
	}
	tok := h.login("Living Room", "alice-pass")
	item := movieID(t, h, tok)
	details := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
	if names := sourceNames(details); len(names) != 2 || names[1] != findReleasesLabel {
		t.Fatalf("versions = %v, want Auto and %q", names, findReleasesLabel)
	}
	find := details.MediaSources[1].ID

	// Selecting it: the client loads the version as an item.
	v := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+find, tok, nil))
	if v.ID != find {
		t.Fatalf("find releases item = %s, want %s", v.ID, find)
	}
	var reopened dto.BaseItemDto
	for deadline := time.Now().Add(5 * time.Second); ; {
		reopened = decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
		if len(reopened.MediaSources) == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if names := sourceNames(reopened); len(names) != 3 || !strings.HasPrefix(names[1], "#1 ") || !strings.HasPrefix(names[2], "#2 ") {
		t.Fatalf("versions after finding = %v, want Auto and the 2 releases", names)
	}
	if searches.Load() != 1 {
		t.Fatalf("searches = %d, want 1", searches.Load())
	}

	// Playing it plays the automatic version (an unsearched episode).
	next := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/NextUp", tok, nil))
	if len(next.Items) == 0 {
		t.Fatal("no next-up episode")
	}
	other := next.Items[0].ID
	od := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+other, tok, nil))
	info := decode[dto.PlaybackInfoResponse](t, h.do(http.MethodPost, "/Items/"+other+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": od.MediaSources[1].ID}))
	if info.ErrorCode != "" || len(info.MediaSources) != 1 || info.PlaySessionID == "" {
		t.Fatalf("playing find releases = %+v, want the automatic version", info)
	}
}

// Browsing never searches: details requests for movies, episodes and series
// start no indexer search and no prequeue, however many items a client loads.
func TestDetailsRequestsDoNotSearch(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: 15})
	installFakePlayback(t, h)
	var searches, prequeues atomic.Int32
	h.srv.deps.Handlers.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
		searches.Add(1)
		writeJSON(w, http.StatusOK, []map[string]any{})
	}
	prequeue := h.srv.deps.Handlers.Prequeue
	h.srv.deps.Handlers.Prequeue = func(w http.ResponseWriter, r *http.Request) {
		prequeues.Add(1)
		prequeue(w, r)
	}
	tok := h.login("Living Room", "alice-pass")
	next := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/NextUp", tok, nil))
	if len(next.Items) == 0 {
		t.Fatal("no next-up episode")
	}
	for _, id := range []string{movieID(t, h, tok), next.Items[0].ID, next.Items[0].SeriesID} {
		for range 3 {
			h.do(http.MethodGet, "/Items/"+id, tok, nil)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if searches.Load() != 0 || prequeues.Load() != 0 {
		t.Fatalf("details requests started %d searches and %d prequeues, want none", searches.Load(), prequeues.Load())
	}
}

func TestReleaseVersionsSetting(t *testing.T) {
	for _, tc := range []struct{ count, want int }{{0, 1}, {3, 4}} { // 1: the item's own source
		h := newHarness(t, config.JellyfinBridgeSettings{ReleaseVersions: tc.count})
		installFakePlayback(t, h)
		searches := 0
		h.srv.deps.Handlers.IndexerSearch = func(w http.ResponseWriter, r *http.Request) {
			searches++
			var results []map[string]any
			for i := 1; i <= 10; i++ {
				results = append(results, map[string]any{"title": fmt.Sprintf("Movie.2024.1080p.WEB.H264-G%02d", i), "guid": fmt.Sprintf("g%d", i)})
			}
			writeJSON(w, http.StatusOK, results)
		}
		tok := h.login("Living Room", "alice-pass")
		item := movieID(t, h, tok)
		h.do(http.MethodPost, "/Items/"+item+"/PlaybackInfo", tok, map[string]any{"MediaSourceId": item})
		var got dto.BaseItemDto
		for deadline := time.Now().Add(5 * time.Second); ; {
			got = decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+item, tok, nil))
			if len(got.MediaSources) >= tc.want || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(got.MediaSources) != tc.want {
			t.Fatalf("count %d: versions = %d, want %d", tc.count, len(got.MediaSources), tc.want)
		}
		if tc.count == 0 && searches != 0 {
			t.Fatalf("count 0 searched releases %d times", searches)
		}
	}
}
