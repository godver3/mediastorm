package jellyfinbridge

import (
	"net/http"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// dupEPG lists every programme twice, as merged XMLTV guides do: once with
// its episode number, once without and with another description.
type dupEPG struct{}

func (dupEPG) GetScheduleMultiple(ids []string, start, end time.Time) map[string][]models.EPGProgram {
	now := time.Now().Truncate(time.Minute)
	out := map[string][]models.EPGProgram{}
	for _, id := range ids {
		first, next := now.Add(-10*time.Minute), now.Add(40*time.Minute)
		out[id] = []models.EPGProgram{
			{ChannelID: id, Title: "Death in Paradise", Description: "Short.", Start: first, Stop: next},
			{ChannelID: id, Title: "Death in Paradise", Description: "A longer description of the case.", Episode: "S12 E2/8", Start: first, Stop: next},
			{ChannelID: id, Title: "Death in Paradise", Description: "The next case, with more detail.", Episode: "S12 E3/8", Start: next, Stop: next.Add(50 * time.Minute)},
			{ChannelID: id, Title: "Death in Paradise", Start: next, Stop: next.Add(50 * time.Minute)},
		}
	}
	return out
}

func TestDuplicateProgrammesMerged(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	h.srv.deps.EPG = dupEPG{}
	h.srv.deps.Handlers.LiveChannels = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"channels": []map[string]string{
			{"id": "ch-1", "name": "Channel One", "url": "http://tv.example/1.ts", "tvgId": "one"},
		}})
	}
	liveChannelCache.mu.Lock()
	liveChannelCache.entries = map[string]channelCacheEntry{}
	liveChannelCache.mu.Unlock()
	tok := h.login("Living Room", "alice-pass")
	channels := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/LiveTv/Channels", tok, nil))
	progs := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodPost, "/LiveTv/Programs", tok, map[string]any{"ChannelIds": []string{channels.Items[0].ID}}))
	if len(progs.Items) != 2 {
		t.Fatalf("programmes = %d, want 2 (one per start): %+v", len(progs.Items), progs.Items)
	}
	if progs.Items[0].ID == progs.Items[1].ID || progs.Items[0].EpisodeTitle != "S12 E2/8" || progs.Items[1].EpisodeTitle != "S12 E3/8" {
		t.Fatalf("kept entries = %+v", progs.Items)
	}
	// The programme's own page resolves to the same, more complete entry.
	detail := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/LiveTv/Programs/"+progs.Items[1].ID, tok, nil))
	if detail.EpisodeTitle != "S12 E3/8" {
		t.Fatalf("programme detail = %+v", detail)
	}
}

func TestDedupeProgramsKeepsOrderAndDetail(t *testing.T) {
	at := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	got := dedupePrograms([]models.EPGProgram{
		{Title: "A", Start: at},
		{Title: "B", Start: at.Add(time.Hour)},
		{Title: "A", Description: "more", Start: at},
		{Title: "A", Description: "even more text", Start: at},
	})
	if len(got) != 2 || got[0].Title != "A" || got[0].Description != "even more text" || got[1].Title != "B" {
		t.Fatalf("dedupe = %+v", got)
	}
	if one := dedupePrograms([]models.EPGProgram{{Title: "x"}}); len(one) != 1 {
		t.Fatal("single programme dropped")
	}
}

// Merged guides also list a programme a minute or two apart, with another
// title spelling (seen on DMAX and TLC in epgshare01's German guide). Those
// pairs collapse as well, and what is left never overlaps.
func TestDedupeProgramsOffsetSources(t *testing.T) {
	at := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	m := func(min int) time.Time { return at.Add(time.Duration(min) * time.Minute) }
	long := "Les Gold verhandelt mit einem Kunden über eine seltene Uhr, und Ashley sucht einen neuen Mitarbeiter."
	got := dedupePrograms([]models.EPGProgram{
		{Title: "Hardcore Pawn - Das härteste Pfandhaus", Description: "kurz", Start: m(-1), Stop: m(28)},
		{Title: "Hardcore Pawn – Das härteste Pfandhaus", Description: long, Start: m(0), Stop: m(25)},
		{Title: "Hardcore Pawn – Das härteste Pfandhaus", Description: long, Start: m(25), Stop: m(55)},
		{Title: "Hardcore Pawn - Das härteste Pfandhaus", Description: "kurz", Start: m(28), Stop: m(58)},
		{Title: "Die Zwangsvollstrecker", Description: "kurz", Start: m(58), Stop: m(113)},
		{Title: "Steel Buddies Spezial", Description: long, Start: m(55), Stop: m(115)},
		{Title: "Nachrichten", Start: m(114), Stop: m(120)},
	})
	if len(got) != 4 {
		t.Fatalf("programmes = %d, want 4: %+v", len(got), got)
	}
	for i, p := range got {
		if p.Description == "kurz" {
			t.Fatalf("entry %d kept the shorter duplicate: %+v", i, p)
		}
		if i > 0 && p.Start.Before(got[i-1].Stop) {
			t.Fatalf("entries %d and %d overlap: %v-%v / %v-%v", i-1, i, got[i-1].Start, got[i-1].Stop, p.Start, p.Stop)
		}
	}
	// The kept "Steel Buddies Spezial" ends where the news starts.
	if got[2].Title != "Steel Buddies Spezial" || !got[2].Stop.Equal(m(114)) || got[3].Title != "Nachrichten" {
		t.Fatalf("timeline = %+v", got)
	}
}
