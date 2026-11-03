package jellyfinbridge

import (
	"context"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/playback"
)

// Chapters: Jellyfin clients read an item's chapters when playback starts
// (jellyfin-web loads the played items with Fields=Chapters before
// PlaybackInfo) and draw them on the seek bar and as next/previous chapter
// buttons. They belong to the stream, which the item does not know until it
// is resolved, so the bridge remembers the chapters of the stream last probed
// for each profile and item, and probes the stream prewarmed for the next
// episode so its chapters are there when the client moves on.

const chapterPrewarmWait = time.Minute

type chapterSet struct {
	chapters []dto.ChapterInfo
	at       time.Time
}

func chaptersKey(profileID, itemID string) string { return profileID + "|" + itemID }

// rememberChapters keeps the chapters of a probed stream.
func (s *Server) rememberChapters(profileID, itemID string, probe *mediaProbe) {
	if probe == nil || profileID == "" || itemID == "" || len(probe.Chapters) < 2 {
		return
	}
	out := make([]dto.ChapterInfo, 0, len(probe.Chapters))
	for i, c := range probe.Chapters {
		name := c.Title
		if name == "" {
			name = "Kapitel " + itoa(i+1)
		}
		out = append(out, dto.ChapterInfo{StartPositionTicks: secondsToTicks(c.Start), Name: name, ImageDateModified: "0001-01-01T00:00:00.0000000Z"})
	}
	s.chapters.Store(chaptersKey(profileID, itemID), chapterSet{chapters: out, at: time.Now()})
}

type knownDuration struct {
	seconds float64
	at      time.Time
}

// rememberDuration keeps the length of a played stream.
func (s *Server) rememberDuration(profileID, itemID string, seconds float64) {
	if profileID == "" || itemID == "" || seconds <= 0 {
		return
	}
	s.durations.Store(chaptersKey(profileID, itemID), knownDuration{seconds: seconds, at: time.Now()})
}

// knownDuration returns the length of the stream last played for an item,
// or 0.
func (s *Server) knownDuration(profileID, itemID string) float64 {
	v, ok := s.durations.Load(chaptersKey(profileID, itemID))
	if !ok {
		return 0
	}
	d := v.(knownDuration)
	if time.Since(d.at) > playSessionTTL {
		s.durations.Delete(chaptersKey(profileID, itemID))
		return 0
	}
	return d.seconds
}

// withStreamDetails adds what is known of the stream to movies and
// episodes: its chapters and its measured length. Metadata runtimes are
// whole minutes, often a minute longer than the file; Plezy treats an end
// more than 10 s before the item's runtime as a dropped stream and reloads
// instead of moving on to the next episode.
func (s *Server) withStreamDetails(p *principal, items []dto.BaseItemDto) {
	for i := range items {
		item := &items[i]
		if item.Type != dto.KindMovie && item.Type != dto.KindEpisode {
			continue
		}
		if seconds := s.knownDuration(p.Profile.ID, item.ID); seconds > 0 {
			item.RunTimeTicks = int64Ptr(secondsToTicks(seconds))
		}
		v, ok := s.chapters.Load(chaptersKey(p.Profile.ID, item.ID))
		if !ok {
			continue
		}
		set := v.(chapterSet)
		if time.Since(set.at) > playSessionTTL {
			s.chapters.Delete(chaptersKey(p.Profile.ID, item.ID))
			continue
		}
		list := make([]any, len(set.chapters))
		for j, c := range set.chapters {
			list[j] = c
		}
		item.Chapters = list
	}
}

// probePrequeued waits for a prewarmed prequeue and probes its stream, so
// the chapters are known before the client presses play.
func (s *Server) probePrequeued(p *principal, itemID, prequeueID string) {
	ctx, cancel := context.WithTimeout(context.Background(), chapterPrewarmWait+mediaProbeWait)
	defer cancel()
	scratch := &playSession{ItemID: itemID, ProfileID: p.Profile.ID, prequeueID: prequeueID}
	if status := s.awaitPrequeue(ctx, p, scratch, chapterPrewarmWait); status != nil && status.Status == playback.PrequeueStatusReady {
		s.probeMedia(ctx, p, scratch, status)
	}
}
