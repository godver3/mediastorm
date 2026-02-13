package epg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
)

const (
	xtreamPerChannelConcurrency = 3
	xtreamPerChannelTimeout     = 30 * time.Second
)

// xtreamStream represents a live stream from the Xtream get_live_streams API.
type xtreamStream struct {
	StreamID     json.Number `json:"stream_id"`
	EPGChannelID string      `json:"epg_channel_id"`
	Name         string      `json:"name"`
}

// xtreamEPGListing represents a single EPG listing from get_simple_data_table.
type xtreamEPGListing struct {
	ID             json.Number `json:"id"`
	EpgID          json.Number `json:"epg_id"`
	Title          string      `json:"title"`
	Description    string      `json:"description"`
	Lang           string      `json:"lang"`
	Start          string      `json:"start"`
	End            string      `json:"end"`
	StartTimestamp json.Number `json:"start_timestamp"`
	StopTimestamp  json.Number `json:"stop_timestamp"`
	ChannelID      string      `json:"channel_id"`
}

// xtreamEPGResponse is the response from get_simple_data_table.
type xtreamEPGResponse struct {
	EPGListings []xtreamEPGListing `json:"epg_listings"`
}

// supplementWithXtreamPerChannel fetches per-channel EPG data from Xtream's
// get_simple_data_table API and merges it into the schedule. This supplements
// the bulk xmltv.php data which often contains stale programmes.
func (s *Service) supplementWithXtreamPerChannel(ctx context.Context, settings *config.Settings, schedule *models.EPGSchedule) error {
	streams, err := s.fetchXtreamStreams(ctx, settings)
	if err != nil {
		return fmt.Errorf("fetch stream list: %w", err)
	}

	// Build map of normalized epgChannelID → streamID for channels that have EPG IDs
	type streamInfo struct {
		streamID     int
		epgChannelID string
	}
	channelStreams := make(map[string]streamInfo)
	for _, stream := range streams {
		if stream.EPGChannelID == "" {
			continue
		}
		sid, err := strconv.Atoi(stream.StreamID.String())
		if err != nil {
			continue
		}
		normalizedID := strings.ToLower(stream.EPGChannelID)
		channelStreams[normalizedID] = streamInfo{
			streamID:     sid,
			epgChannelID: normalizedID,
		}
	}

	if len(channelStreams) == 0 {
		log.Printf("[epg] no streams with EPG channel IDs found, skipping per-channel supplement")
		return nil
	}

	log.Printf("[epg] supplementing %d channels with per-channel EPG data", len(channelStreams))

	// Semaphore for concurrency control
	sem := make(chan struct{}, xtreamPerChannelConcurrency)
	var mu sync.Mutex
	var wg sync.WaitGroup

	succeeded := 0
	failed := 0
	programsAdded := 0

	for _, info := range channelStreams {
		wg.Add(1)
		go func(si streamInfo) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			programs, err := s.fetchChannelEPG(ctx, settings, si.streamID)
			if err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}

			// Set the channel ID on all programs
			for i := range programs {
				programs[i].ChannelID = si.epgChannelID
			}

			mu.Lock()
			existing := schedule.Programs[si.epgChannelID]
			merged := mergePrograms(existing, programs)
			schedule.Programs[si.epgChannelID] = merged
			programsAdded += len(programs)
			succeeded++
			mu.Unlock()
		}(info)
	}

	wg.Wait()

	log.Printf("[epg] per-channel supplement complete: %d/%d channels succeeded, %d programs added",
		succeeded, succeeded+failed, programsAdded)

	return nil
}

// fetchXtreamStreams fetches the live stream list from Xtream's player API.
func (s *Service) fetchXtreamStreams(ctx context.Context, settings *config.Settings) ([]xtreamStream, error) {
	host := strings.TrimRight(settings.Live.XtreamHost, "/")
	apiURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s&action=get_live_streams",
		host, url.QueryEscape(settings.Live.XtreamUsername), url.QueryEscape(settings.Live.XtreamPassword))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch streams: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("streams API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var streams []xtreamStream
	if err := json.Unmarshal(body, &streams); err != nil {
		return nil, fmt.Errorf("decode streams: %w", err)
	}

	return streams, nil
}

// fetchChannelEPG fetches EPG data for a single channel from Xtream's get_simple_data_table API.
func (s *Service) fetchChannelEPG(ctx context.Context, settings *config.Settings, streamID int) ([]models.EPGProgram, error) {
	host := strings.TrimRight(settings.Live.XtreamHost, "/")
	apiURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s&action=get_simple_data_table&stream_id=%d",
		host, url.QueryEscape(settings.Live.XtreamUsername), url.QueryEscape(settings.Live.XtreamPassword), streamID)

	ctx, cancel := context.WithTimeout(ctx, xtreamPerChannelTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch channel EPG: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("channel EPG API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024)) // 1MB limit per channel
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var epgResp xtreamEPGResponse
	if err := json.Unmarshal(body, &epgResp); err != nil {
		return nil, fmt.Errorf("decode EPG response: %w", err)
	}

	// Convert listings to EPGProgram, deduplicating as we go
	seen := make(map[string]bool)
	var programs []models.EPGProgram

	for _, listing := range epgResp.EPGListings {
		startTS, err := strconv.ParseInt(listing.StartTimestamp.String(), 10, 64)
		if err != nil || startTS == 0 {
			continue
		}
		stopTS, err := strconv.ParseInt(listing.StopTimestamp.String(), 10, 64)
		if err != nil || stopTS == 0 {
			continue
		}

		start := time.Unix(startTS, 0).UTC()
		stop := time.Unix(stopTS, 0).UTC()

		title := decodeBase64Safe(listing.Title)
		if title == "" {
			continue
		}
		description := decodeBase64Safe(listing.Description)

		// Deduplicate by title+start+stop
		key := fmt.Sprintf("%s|%d|%d", title, startTS, stopTS)
		if seen[key] {
			continue
		}
		seen[key] = true

		programs = append(programs, models.EPGProgram{
			Title:       title,
			Description: description,
			Start:       start,
			Stop:        stop,
		})
	}

	sort.Slice(programs, func(i, j int) bool {
		return programs[i].Start.Before(programs[j].Start)
	})

	return programs, nil
}

// mergePrograms merges per-channel EPG data into existing programmes.
// Per-channel data is considered fresher — for the time range it covers,
// it replaces existing data. Existing data outside that range is preserved.
func mergePrograms(existing, perChannel []models.EPGProgram) []models.EPGProgram {
	if len(perChannel) == 0 {
		return existing
	}
	if len(existing) == 0 {
		return perChannel
	}

	// Find the time range covered by per-channel data
	minStart := perChannel[0].Start
	maxStop := perChannel[0].Stop
	for _, p := range perChannel[1:] {
		if p.Start.Before(minStart) {
			minStart = p.Start
		}
		if p.Stop.After(maxStop) {
			maxStop = p.Stop
		}
	}

	// Keep existing programmes outside the per-channel time range
	var merged []models.EPGProgram
	for _, p := range existing {
		if p.Stop.Before(minStart) || p.Stop.Equal(minStart) ||
			p.Start.After(maxStop) || p.Start.Equal(maxStop) {
			merged = append(merged, p)
		}
	}

	// Add all per-channel programmes
	merged = append(merged, perChannel...)

	// Sort by start time
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Start.Before(merged[j].Start)
	})

	// Deduplicate by (channelID, title, start, stop)
	if len(merged) > 1 {
		deduped := merged[:1]
		for i := 1; i < len(merged); i++ {
			prev := deduped[len(deduped)-1]
			curr := merged[i]
			if curr.ChannelID == prev.ChannelID &&
				curr.Title == prev.Title &&
				curr.Start.Equal(prev.Start) &&
				curr.Stop.Equal(prev.Stop) {
				continue
			}
			deduped = append(deduped, curr)
		}
		merged = deduped
	}

	return merged
}

// decodeBase64Safe attempts to base64-decode a string.
// If decoding fails, returns the original string (it may already be plain text).
func decodeBase64Safe(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return s
	}
	return string(decoded)
}
