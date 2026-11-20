package jellyfinbridge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// episodeNumberPattern reads the guide's episode numbers: "S12 E2/8",
// "S01E05", "E5", "S3" (onscreen and xmltv_ns as mediastorm renders them).
var episodeNumberPattern = regexp.MustCompile(`(?i)^\s*(?:S\s*(\d{1,4}))?\s*(?:E\s*(\d{1,5}))?`)

// episodeNumbers returns the season and episode of a guide episode number
// (0 when absent).
func episodeNumbers(raw string) (season, episode int) {
	m := episodeNumberPattern.FindStringSubmatch(raw)
	if m == nil {
		return 0, 0
	}
	season, _ = strconv.Atoi(m[1])
	episode, _ = strconv.Atoi(m[2])
	return season, episode
}

// setEpisodeNumbers fills ParentIndexNumber/IndexNumber, which Jellyfin
// clients show as "S1:E5" next to the episode title.
func setEpisodeNumbers(item *dto.BaseItemDto, raw string) {
	season, episode := episodeNumbers(raw)
	if season > 0 {
		item.ParentIndexNumber = intPtr(season)
	}
	if episode > 0 {
		item.IndexNumber = intPtr(episode)
	}
}

// applyRecordingGuide gives a recording what the guide said about its
// programme: episode numbers and title, year, genres and rating.
//
// Recordings are Video items (a home videos library, which Plezy needs), and
// Jellyfin clients only render "S1:E2 - episode title" for episodes,
// programmes and recordings. So the episode goes into the name, which every
// client shows ("Death in Paradise – S12:E2 – Mord im Paradies"), and the
// sort name keeps a series' episodes in order. EpisodeTitle and IsSeries
// stay unset: on a Video item jellyfin-web would repeat the name above the
// title.
func applyRecordingGuide(item *dto.BaseItemDto, rec models.Recording) {
	g := rec.Guide
	if g == nil {
		return
	}
	season, episode := episodeNumbers(g.Episode)
	if season > 0 {
		item.ParentIndexNumber = intPtr(season)
	}
	if episode > 0 {
		item.IndexNumber = intPtr(episode)
	}
	parts := []string{rec.Title}
	switch {
	case season > 0 && episode > 0:
		parts = append(parts, fmt.Sprintf("S%d:E%d", season, episode))
	case episode > 0:
		parts = append(parts, fmt.Sprintf("E%d", episode))
	}
	if t := strings.TrimSpace(g.EpisodeTitle); t != "" && !strings.EqualFold(t, rec.Title) {
		parts = append(parts, t)
	}
	if len(parts) > 1 {
		item.Name = strings.Join(parts, " – ")
		item.SortName = fmt.Sprintf("%s %04d %05d", sortName(rec.Title), season, episode)
	}
	if g.Year > 0 {
		item.ProductionYear = intPtr(g.Year)
	}
	if len(g.Categories) > 0 {
		item.Genres = g.Categories
	}
	item.OfficialRating = g.Rating
}

// recordingSource is the media source a recording item lists, so the
// details page shows the file's container and size (jellyfin-web's media
// info reads them from the item). A running recording has its current size
// and no length yet; PlaybackInfo still describes the stream itself.
func recordingSource(itemID string, rec models.Recording, runtime *int64) (dto.MediaSourceInfo, bool) {
	path := strings.TrimSpace(rec.OutputPath)
	if path == "" {
		return dto.MediaSourceInfo{}, false
	}
	container := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if container == "" {
		container = "ts"
	}
	size := rec.OutputSizeBytes
	running := rec.Status == models.RecordingStatusRunning || rec.Status == models.RecordingStatusStarting
	if info, err := os.Stat(path); err == nil && (running || size <= 0) {
		size = info.Size()
	}
	if running {
		runtime = nil
	}
	source := versionStub(itemID, rec.ChannelName, runtime)
	source.Container = container
	if size > 0 {
		source.Size = int64Ptr(size)
	}
	return source, true
}
