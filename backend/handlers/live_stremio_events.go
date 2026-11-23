package handlers

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

// Stremio sports addons (e.g. Highfly) list matches, not 24/7 channels, and
// have no XMLTV guide. Each match carries its start time as text
// ("23 Nov 2026 · 19:45 UTC") in releaseInfo — or, once the match is live, only
// in its meta resource (the catalog then says "LIVE"). This file turns those
// start times into synthetic guide entries keyed by the channel's TvgID (the
// Stremio meta ID), which the EPG handler serves when no XMLTV entry exists.

const (
	stremioEventDefaultDuration = 3 * time.Hour
	// A match the catalog still lists as LIVE past its estimated end keeps
	// running for at least this long after the last catalog refresh.
	stremioEventLiveGrace = 15 * time.Minute
	// Events are dropped this long after they ended.
	stremioEventRetention = 12 * time.Hour
	// Meta lookups for LIVE items without a time in the catalog.
	stremioEventMetaWorkers  = 4
	stremioEventMetaPerFetch = 80
	stremioEventMetaTimeout  = 10 * time.Second
)

var stremioEventTimePattern = regexp.MustCompile(`(\d{1,2} [A-Za-z]{3} \d{4}) · (\d{1,2}:\d{2}) UTC`)

// parseStremioEventTime pulls a "2 Jan 2006 · 15:04 UTC" start time out of free text.
func parseStremioEventTime(texts ...string) (time.Time, bool) {
	for _, text := range texts {
		m := stremioEventTimePattern.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		t, err := time.Parse("2 Jan 2006 15:04", m[1]+" "+m[2])
		if err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// stremioMetaEventTime finds an event start: the standard Stremio "released"
// ISO timestamp (only for non-VOD types, where it is an air time rather than
// a release date), else a "· HH:MM UTC" string in releaseInfo/description.
func stremioMetaEventTime(meta stremioMeta) (time.Time, bool) {
	switch strings.ToLower(strings.TrimSpace(meta.Type)) {
	case "movie", "series":
	default:
		if raw := strings.TrimSpace(meta.Released); raw != "" {
			if t, err := time.Parse(time.RFC3339, raw); err == nil && !t.IsZero() && (t.Hour() != 0 || t.Minute() != 0) {
				return t.UTC(), true
			}
		}
	}
	return parseStremioEventTime(meta.ReleaseInfo, meta.Description)
}

func stremioMetaIsLive(meta stremioMeta) bool {
	return strings.EqualFold(strings.TrimSpace(meta.ReleaseInfo), "LIVE")
}

// stremioEventDuration estimates a match length from the sport, since the
// addon gives no end time. Sport is matched against the genres and the ID
// ("streamed:american-football-…").
func stremioEventDuration(meta stremioMeta) time.Duration {
	haystack := strings.ToLower(meta.ID + " " + strings.Join(meta.Genres, " "))
	for _, rule := range []struct {
		keys []string
		d    time.Duration
	}{
		{[]string{"american-football", "american football", "nfl"}, 3*time.Hour + 30*time.Minute},
		{[]string{"cricket"}, 4 * time.Hour},
		{[]string{"golf"}, 5 * time.Hour},
		{[]string{"motor", "f1", "formula", "nascar"}, 2*time.Hour + 30*time.Minute},
		{[]string{"fight", "ufc", "boxing", "mma"}, 4 * time.Hour},
		{[]string{"baseball", "mlb"}, 3 * time.Hour},
		{[]string{"basketball", "nba"}, 2*time.Hour + 30*time.Minute},
		{[]string{"hockey", "nhl"}, 2*time.Hour + 30*time.Minute},
		{[]string{"afl"}, 2*time.Hour + 45*time.Minute},
		{[]string{"rugby"}, 2 * time.Hour},
		{[]string{"football", "soccer"}, 2 * time.Hour},
		{[]string{"tennis"}, 3 * time.Hour},
		{[]string{"darts", "billiards", "snooker"}, 3 * time.Hour},
	} {
		for _, key := range rule.keys {
			if strings.Contains(haystack, key) {
				return rule.d
			}
		}
	}
	return stremioEventDefaultDuration
}

type stremioEvent struct {
	program  models.EPGProgram
	duration time.Duration
	liveSeen time.Time // last catalog refresh that listed it as LIVE
}

type stremioEventIndex struct {
	mu     sync.RWMutex
	events map[string]*stremioEvent // lowercase meta ID
	noTime map[string]bool          // meta IDs whose meta resource has no start time
}

func (idx *stremioEventIndex) init() {
	if idx.events == nil {
		idx.events = make(map[string]*stremioEvent)
		idx.noTime = make(map[string]bool)
	}
}

// record stores an event with a known start time.
func (idx *stremioEventIndex) record(meta stremioMeta, start time.Time, live bool, now time.Time) {
	key := strings.ToLower(strings.TrimSpace(meta.ID))
	if key == "" {
		return
	}
	duration := stremioEventDuration(meta)
	icon := strings.TrimSpace(meta.Poster)
	if icon == "" {
		icon = strings.TrimSpace(meta.Background)
	}
	var categories []string
	for _, g := range meta.Genres {
		if g = strings.TrimSpace(g); g != "" {
			categories = append(categories, g)
		}
	}
	ev := &stremioEvent{
		program: models.EPGProgram{
			ChannelID:   strings.TrimSpace(meta.ID),
			Title:       strings.TrimSpace(meta.Name),
			Description: stremioEventDescription(meta),
			Start:       start,
			Stop:        start.Add(duration),
			Icon:        icon,
			Categories:  categories,
			New:         true,
		},
		duration: duration,
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.init()
	if prev, ok := idx.events[key]; ok && !live {
		ev.liveSeen = prev.liveSeen
	}
	if live {
		ev.liveSeen = now
	}
	idx.events[key] = ev
}

// stremioEventDescription keeps the description minus the lines that only
// repeat the title or status.
func stremioEventDescription(meta stremioMeta) string {
	name := strings.TrimSpace(meta.Name)
	var kept []string
	for _, line := range strings.Split(meta.Description, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == name || strings.EqualFold(line, "LIVE NOW") || stremioEventTimePattern.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// markLive refreshes the LIVE timestamp of a known event; it reports whether
// the event (or a negative "no time" entry) was already known.
func (idx *stremioEventIndex) markLive(id string, now time.Time) bool {
	key := strings.ToLower(strings.TrimSpace(id))
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.init()
	if ev, ok := idx.events[key]; ok {
		ev.liveSeen = now
		return true
	}
	return idx.noTime[key]
}

func (idx *stremioEventIndex) markNoTime(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.init()
	idx.noTime[strings.ToLower(strings.TrimSpace(id))] = true
}

func (idx *stremioEventIndex) prune(now time.Time) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for key, ev := range idx.events {
		if ev.stop(now).Add(stremioEventRetention).Before(now) {
			delete(idx.events, key)
		}
	}
	if len(idx.noTime) > 5000 {
		idx.noTime = make(map[string]bool)
	}
}

// stop is the estimated end, stretched while the catalog still says LIVE.
func (ev *stremioEvent) stop(now time.Time) time.Time {
	stop := ev.program.Start.Add(ev.duration)
	if !ev.liveSeen.IsZero() && now.Sub(ev.liveSeen) < stremioChannelsMaxStale {
		if grace := ev.liveSeen.Add(stremioEventLiveGrace); grace.After(stop) {
			stop = grace
		}
	}
	return stop
}

// programs returns the synthetic guide entry for each requested ID that
// overlaps [start, end), keyed by the requested ID as given.
func (idx *stremioEventIndex) programs(ids []string, start, end time.Time) map[string][]models.EPGProgram {
	now := time.Now().UTC()
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make(map[string][]models.EPGProgram)
	for _, id := range ids {
		ev, ok := idx.events[strings.ToLower(strings.TrimSpace(id))]
		if !ok {
			continue
		}
		p := ev.program
		p.ChannelID = id
		p.Stop = ev.stop(now)
		if p.Stop.After(start) && p.Start.Before(end) {
			out[id] = []models.EPGProgram{p}
		}
	}
	return out
}

// recordStremioEvents indexes catalog metas and, in the background, looks up
// the start time of LIVE items whose catalog entry has none.
func (h *LiveHandler) recordStremioEvents(client *http.Client, baseURL string, metas []stremioMeta) {
	now := time.Now().UTC()
	idx := &h.stremioEvents
	idx.prune(now)
	var lookups []stremioMeta
	for _, meta := range metas {
		live := stremioMetaIsLive(meta)
		if start, ok := stremioMetaEventTime(meta); ok {
			idx.record(meta, start, live, now)
			continue
		}
		if live && !idx.markLive(meta.ID, now) && len(lookups) < stremioEventMetaPerFetch {
			lookups = append(lookups, meta)
		}
	}
	if len(lookups) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), stremioRefreshTimeout)
		defer cancel()
		jobs := make(chan stremioMeta)
		var wg sync.WaitGroup
		var mu sync.Mutex
		found := 0
		for range stremioEventMetaWorkers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for meta := range jobs {
					var resp struct {
						Meta stremioMeta `json:"meta"`
					}
					reqCtx, reqCancel := context.WithTimeout(ctx, stremioEventMetaTimeout)
					endpoint := baseURL + "/meta/" + url.PathEscape(meta.Type) + "/" + url.PathEscape(meta.ID) + ".json"
					err := getStremioJSON(reqCtx, client, endpoint, &resp)
					reqCancel()
					if err != nil {
						continue // transient: try again on the next catalog refresh
					}
					start, ok := stremioMetaEventTime(resp.Meta)
					if !ok {
						idx.markNoTime(meta.ID)
						continue
					}
					merged := meta
					if len(resp.Meta.Genres) > 0 {
						merged.Genres = resp.Meta.Genres
					}
					idx.record(merged, start, true, time.Now().UTC())
					mu.Lock()
					found++
					mu.Unlock()
				}
			}()
		}
		for _, meta := range lookups {
			select {
			case jobs <- meta:
			case <-ctx.Done():
			}
		}
		close(jobs)
		wg.Wait()
		log.Printf("[live][stremio] event times: looked up %d live items, %d had a start time", len(lookups), found)
	}()
}

// StremioEventPrograms implements stremioEventProgramProvider for the EPG handler.
func (h *LiveHandler) StremioEventPrograms(ids []string, start, end time.Time) map[string][]models.EPGProgram {
	return h.stremioEvents.programs(ids, start, end)
}

// stremioEventProgramProvider is satisfied by LiveHandler; the EPG handler
// uses it to fill guide gaps for Stremio event channels.
type stremioEventProgramProvider interface {
	StremioEventPrograms(ids []string, start, end time.Time) map[string][]models.EPGProgram
	HasStremioEvents() bool
}

// HasStremioEvents implements stremioEventProgramProvider.
func (h *LiveHandler) HasStremioEvents() bool {
	h.stremioEvents.mu.RLock()
	defer h.stremioEvents.mu.RUnlock()
	return len(h.stremioEvents.events) > 0
}
