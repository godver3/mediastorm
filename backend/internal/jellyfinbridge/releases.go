package jellyfinbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
	"novastream/services/playback"
	"novastream/utils/parsett"
)

// Release choice: movies and episodes offer mediastorm's search results as
// Jellyfin versions ("media sources"), so a client's version picker can play
// a specific release. The first version is the automatic pick (the prequeue,
// as before); the others are the best releases in mediastorm's own ranking.
const (
	// releaseListSize is how many releases the search keeps: the versions
	// shown (Release Versions setting) come from it, and Auto: Avoid Video
	// Transcoding walks it even when no versions are shown.
	releaseListSize    = 15
	maxReleaseVersions = 50
	releaseListTTL     = 30 * time.Minute
	// releaseRetryTTL keeps a failed or empty search (an indexer outage) only
	// briefly.
	releaseRetryTTL   = time.Minute
	releaseSearchWait = 45 * time.Second
	releaseQueueWait  = 2 * time.Minute
)

// release is one offered search result.
type release struct {
	Key    string          // stable per result (GUID / download URL)
	Label  string          // version name shown by clients
	Result json.RawMessage // the result as mediastorm returned it, passed back to resolve
	Title  string
	Parsed *parsett.ParsedTitle // codec, HDR, group... from the release name (nil if unparsed)
}

type releaseList struct {
	items   []release
	ready   bool
	done    chan struct{} // closed when the search finished
	fetched time.Time
	err     error
	gen     uint64 // ranking generation the search ran under
}

type releaseCache struct {
	mu       sync.Mutex
	lists    map[string]*releaseList // profile|item
	versions map[string]string       // version ID -> item ID
}

func newReleaseCache() *releaseCache {
	return &releaseCache{lists: map[string]*releaseList{}, versions: map[string]string{}}
}

// versionItem maps a release version ID to its item. Jellyfin clients treat
// versions as items of their own: jellyfin-web and Android TV load the
// selected version with GET /Items/{versionId} and then play, report and
// fetch images under that ID (see versionAliases). Version IDs are HMACs per
// profile, device and item, so the map needs no profile scope, and
// token-less media requests can use it.
func (s *Server) versionItem(id string) (string, bool) {
	s.releases.mu.Lock()
	defer s.releases.mu.Unlock()
	itemID, ok := s.releases.versions[normalizeID(id)]
	return itemID, ok
}

// releasesFor returns the releases known for an item (none while the search
// runs) and starts a search when there is no fresh list.
func (s *Server) releasesFor(p *principal, itemID string, ref itemRef) []release {
	return s.releasesWait(p, itemID, ref, 0)
}

// releaseVersionCount is how many releases are offered as versions.
func (s *Server) releaseVersionCount() int {
	return min(max(s.settings().ReleaseVersions, 0), maxReleaseVersions)
}

// shownReleases cuts a release list to the versions offered.
func (s *Server) shownReleases(list []release) []release {
	return list[:min(len(list), s.releaseVersionCount())]
}

// prefetchReleases starts the release search for an item a client is likely
// to open next.
func (s *Server) prefetchReleases(p *principal, itemID string) {
	if s.releaseVersionCount() == 0 {
		return
	}
	if ref, ok := s.ids.get(itemID); ok {
		s.releasesFor(p, itemID, ref)
	}
}

// releasesWait is releasesFor waiting up to wait for a running search.
func (s *Server) releasesWait(p *principal, itemID string, ref itemRef, wait time.Duration) []release {
	if (ref.Kind != refMovie && ref.Kind != refEpisode) || s.deps.Handlers.IndexerSearch == nil {
		return nil
	}
	key := p.Profile.ID + "|" + itemID
	s.releases.mu.Lock()
	gen := s.rankingGeneration()
	list, ok := s.releases.lists[key]
	stale := ok && ((list.ready && time.Since(list.fetched) > list.ttl()) || list.gen != gen)
	if !ok || stale {
		for k, old := range s.releases.lists {
			if old.ready && time.Since(old.fetched) > old.ttl() {
				delete(s.releases.lists, k)
			}
		}
		if len(s.releases.versions) > 50_000 {
			s.releases.versions = map[string]string{}
		}
		list = &releaseList{done: make(chan struct{}), gen: gen}
		s.releases.lists[key] = list
		go s.searchReleases(p, ref, list)
	}
	ready, done := list.ready, list.done
	s.releases.mu.Unlock()
	if !ready && wait > 0 {
		select {
		case <-done:
		case <-time.After(wait):
		}
	}
	s.releases.mu.Lock()
	defer s.releases.mu.Unlock()
	return list.items
}

func (s *Server) searchReleases(p *principal, ref itemRef, list *releaseList) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseSearchWait)
	defer cancel()
	items, err := s.fetchReleases(ctx, p, ref)
	if err != nil {
		log.Printf("[jellyfin-bridge] release search for %s: %v", ref.Source, err)
	}
	s.releases.mu.Lock()
	list.items, list.err, list.ready, list.fetched = items, err, true, time.Now()
	s.releases.mu.Unlock()
	close(list.done)
}

// fetchReleases runs mediastorm's indexer search the way the prequeue does
// (same title, episode code, IDs and profile) and keeps the first results
// that passed the profile's filters, in mediastorm's ranking order.
func (s *Server) fetchReleases(ctx context.Context, p *principal, ref itemRef) ([]release, error) {
	req, err := s.prequeueRequest(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	query := req.TitleName
	if req.MediaType == "series" {
		query = fmt.Sprintf("%s S%02dE%02d", req.TitleName, req.SeasonNumber, req.EpisodeNumber)
	}
	q := url.Values{"q": {query}, "mediaType": {req.MediaType}, "limit": {"50"}, "userId": {p.Profile.ID}, "titleId": {req.TitleID}}
	if req.ImdbID != "" {
		q.Set("imdbId", req.ImdbID)
	}
	if req.Year > 0 {
		q.Set("year", strconv.Itoa(req.Year))
	}
	if p.Client.DeviceID != "" {
		q.Set("clientId", "jellyfin-"+p.Client.DeviceID)
	}
	var results []json.RawMessage
	if _, err := callJSON(s.deps.Handlers.IndexerSearch, internalRequest(ctx, p, http.MethodGet, "/api/indexers/search?"+q.Encode(), nil, nil), &results); err != nil {
		return nil, err
	}
	var out []release
	for _, raw := range results {
		var r struct {
			models.NZBResult
			FilterStatus string `json:"filterStatus"`
		}
		if json.Unmarshal(raw, &r) != nil || r.FilterStatus == "filtered" || strings.TrimSpace(r.Title) == "" {
			continue
		}
		parsed, _ := parsett.ParseTitle(r.Title)
		out = append(out, release{Key: releaseKey(r.NZBResult), Label: releaseLabel(len(out)+1, r.NZBResult, parsed), Result: raw, Title: strings.TrimSpace(r.Title), Parsed: parsed})
		if len(out) == max(releaseListSize, s.releaseVersionCount()) {
			break
		}
	}
	return out, nil
}

func releaseKey(r models.NZBResult) string {
	sum := sha256.Sum256([]byte(r.GUID + "|" + r.DownloadURL + "|" + r.Link + "|" + r.Title))
	return hex.EncodeToString(sum[:8])
}

// releaseLabel names a release in version pickers from its parsed name:
// "#1 2160p HEVC DV HDR10+ · BluRay REMUX · TrueHD Atmos 7.1 · DE · FGT · 58.2 GB".
// Without a usable parse the release name stands in.
func releaseLabel(rank int, r models.NZBResult, parsed *parsett.ParsedTitle) string {
	var parts []string
	if parsed != nil {
		var video []string
		if parsed.Resolution != "" {
			video = append(video, parsed.Resolution)
		}
		if codec := codecLabel(parsed.Codec); codec != "" {
			video = append(video, codec)
		}
		video = append(video, parsed.HDR...)
		if len(video) > 0 {
			parts = append(parts, strings.Join(video, " "))
		}
		if parsed.Quality != "" {
			parts = append(parts, parsed.Quality)
		}
		var audio []string
		for _, a := range parsed.Audio {
			audio = append(audio, audioLabel(a))
		}
		audio = append(audio, parsed.Channels...)
		if len(audio) > 0 {
			parts = append(parts, strings.Join(audio, " "))
		}
		if len(parsed.Languages) > 0 {
			langs := make([]string, 0, len(parsed.Languages))
			for _, l := range parsed.Languages {
				langs = append(langs, strings.ToUpper(l))
			}
			parts = append(parts, strings.Join(langs, "/"))
		}
		if parsed.Group != "" {
			parts = append(parts, parsed.Group)
		}
	}
	if len(parts) < 2 {
		parts = []string{strings.TrimSpace(r.Title)}
	}
	if size := r.EffectiveItemSizeBytes(); size > 0 {
		parts = append(parts, formatSize(size))
	}
	return fmt.Sprintf("#%d ", rank) + strings.Join(parts, " · ")
}

func codecLabel(codec string) string {
	switch strings.ToLower(codec) {
	case "":
		return ""
	case "avc":
		return "H.264"
	case "hevc":
		return "HEVC"
	}
	return strings.ToUpper(codec)
}

var audioLabels = map[string]string{
	"Dolby Digital Plus": "DD+", "Dolby Digital": "DD", "Dolby TrueHD": "TrueHD",
	"DTS Lossless": "DTS-HD MA", "DTS Lossy": "DTS",
}

func audioLabel(a string) string {
	if short, ok := audioLabels[a]; ok {
		return short
	}
	return a
}

func formatSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%d MB", b/(1<<20))
	}
	return fmt.Sprintf("%d KB", b/1024)
}

// releaseMediaSourceID is the media source ID of a release version.
func (s *Server) releaseMediaSourceID(p *principal, itemID, key string) string {
	return s.mediaSourceID(p.Profile.ID, p.Client.DeviceID, itemID+"|release|"+key)
}

// releaseByMediaSource finds the release a media source ID stands for.
func (s *Server) releaseByMediaSource(p *principal, itemID string, ref itemRef, mediaSourceID string) (release, bool) {
	mediaSourceID = strings.ToLower(strings.TrimSpace(mediaSourceID))
	if mediaSourceID == "" {
		return release{}, false
	}
	if s.releaseVersionCount() == 0 {
		return release{}, false
	}
	for _, r := range s.shownReleases(s.releasesFor(p, itemID, ref)) {
		if s.releaseMediaSourceID(p, itemID, r.Key) == mediaSourceID {
			return r, true
		}
	}
	return release{}, false
}

// releasesCached returns a fresh release list found earlier for the item,
// without starting a search.
func (s *Server) releasesCached(p *principal, itemID string) []release {
	s.releases.mu.Lock()
	defer s.releases.mu.Unlock()
	list, ok := s.releases.lists[p.Profile.ID+"|"+itemID]
	if !ok || !list.ready || time.Since(list.fetched) > list.ttl() || list.gen != s.rankingGeneration() {
		return nil
	}
	return list.items
}

// itemVersions lists an item's versions for clients: the automatic pick
// first, then the releases found so far. It never searches: clients fetch
// items in bulk while browsing, so the list comes from playback's search.
func (s *Server) itemVersions(p *principal, item *dto.BaseItemDto, ref itemRef) {
	if (ref.Kind != refMovie && ref.Kind != refEpisode) || s.releaseVersionCount() == 0 {
		return
	}
	releases := s.shownReleases(s.releasesCached(p, item.ID))
	if len(releases) == 0 {
		return
	}
	// The default version's ID is the item ID, as in Jellyfin: clients only
	// load a version as a separate item when its ID differs.
	versions := []dto.MediaSourceInfo{versionStub(item.ID, "Auto (mediastorm)", item.RunTimeTicks)}
	s.releases.mu.Lock()
	for _, r := range releases {
		id := s.releaseMediaSourceID(p, item.ID, r.Key)
		s.releases.versions[id] = item.ID
		versions = append(versions, versionStub(id, r.Label, item.RunTimeTicks))
	}
	s.releases.mu.Unlock()
	item.MediaSources = versions
}

func versionStub(id, name string, runtime *int64) dto.MediaSourceInfo {
	return dto.MediaSourceInfo{
		Protocol: "File", ID: id, Type: "Default", Container: "mkv", Name: name, RunTimeTicks: runtime,
		SupportsTranscoding: true, MediaStreams: []dto.MediaStream{}, MediaAttachments: []any{},
		Formats: []string{}, RequiredHTTPHeaders: map[string]string{}, VideoType: "VideoFile", ETag: id,
	}
}

// resolveRelease plays a chosen release: mediastorm resolves it (waiting in
// its queue when the download engine needs to), the file is probed, and the
// play session gets a prequeue-shaped status from that.
func (s *Server) resolveRelease(p *principal, sess *playSession, r release, titleID string) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseQueueWait+30*time.Second)
	defer cancel()
	status, err := s.resolveReleaseStatus(ctx, p, sess, r, titleID)
	if err != nil {
		log.Printf("[jellyfin-bridge] release %s for %s: %v", r.Title, sess.ItemID, err)
		status = &playback.PrequeueStatusResponse{Status: playback.PrequeueStatusFailed, Error: err.Error()}
	} else {
		log.Printf("[jellyfin-bridge] release %q resolved for %s", r.Title, sess.ItemID)
	}
	sess.mu.Lock()
	sess.status = status
	if status.Duration > 0 {
		sess.duration = status.Duration
	}
	sess.mu.Unlock()
}

// resolveReleaseStatus resolves a release and describes it like a ready
// prequeue; the probe is cached on sess.
func (s *Server) resolveReleaseStatus(ctx context.Context, p *principal, sess *playSession, r release, titleID string) (*playback.PrequeueStatusResponse, error) {
	body, _ := json.Marshal(map[string]any{
		"result": r.Result, "profileId": p.Profile.ID, "includeTrackSelection": true,
		"titleId": titleID, "clientId": "jellyfin-" + p.Client.DeviceID,
	})
	var res models.PlaybackResolution
	if _, err := callJSON(s.deps.Handlers.PlaybackResolve, internalRequest(ctx, p, http.MethodPost, "/api/playback/resolve", body, nil), &res); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(releaseQueueWait)
	for strings.TrimSpace(res.WebDAVPath) == "" && res.QueueID > 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
		id := strconv.FormatInt(res.QueueID, 10)
		q := url.Values{"includeTrackSelection": {"true"}, "profileId": {p.Profile.ID}, "titleId": {titleID}}
		var next models.PlaybackResolution
		if _, err := callJSON(s.deps.Handlers.PlaybackQueue, internalRequest(ctx, p, http.MethodGet, "/api/playback/queue/"+id+"?"+q.Encode(), nil, map[string]string{"queueID": id}), &next); err != nil {
			return nil, err
		}
		res = next
	}
	if strings.TrimSpace(res.WebDAVPath) == "" {
		return nil, fmt.Errorf("release did not become playable")
	}

	var result models.NZBResult
	_ = json.Unmarshal(r.Result, &result)
	status := &playback.PrequeueStatusResponse{
		Status: playback.PrequeueStatusReady, StreamPath: res.WebDAVPath, DisplayName: result.Title,
		FileSize: res.FileSize, ServiceType: string(result.ServiceType),
		SelectedAudioTrack: res.TrackSelection.AudioStreamIndex(), SelectedSubtitleTrack: -1,
		TrackSelection: res.TrackSelection,
	}
	if status.FileSize <= 0 {
		status.FileSize = result.EffectiveItemSizeBytes()
	}
	if probe := s.probeMedia(ctx, p, sess, status); probe != nil {
		probe.fillStatus(status)
	}
	if sub := res.TrackSelection.SubtitleStreamIndex(); sub >= 0 {
		status.SelectedSubtitleTrack = sub
	}
	return status, nil
}

// prefetchNextEpisode searches the releases of the episode after itemID
// while itemID plays, so its versions are there when the client moves on.
func (s *Server) prefetchNextEpisode(p *principal, itemID string) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseSearchWait)
	defer cancel()
	item, ok := s.itemByID(ctx, p, nil, itemID, false)
	if !ok || item.SeriesID == "" {
		return
	}
	series, ok := s.ids.get(item.SeriesID)
	if !ok || series.Kind != refSeries {
		return
	}
	episodes := s.seriesEpisodes(ctx, s.newUserState(p.Profile.ID), s.seriesRefFrom(titleFromRef(series)), -1)
	for i, ep := range episodes {
		if ep.ID == itemID && i+1 < len(episodes) {
			s.prefetchReleases(p, episodes[i+1].ID)
			// Resolve and probe it too: it starts sooner, and its measured
			// length is the runtime clients see when they move on to it.
			s.prewarm(p, episodes[i+1].ID)
			return
		}
	}
}

// ttl is how long a finished search stays valid.
func (l *releaseList) ttl() time.Duration {
	if l.err != nil || len(l.items) == 0 {
		return releaseRetryTTL
	}
	return releaseListTTL
}
