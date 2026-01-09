package handlers

import (
	"context"
	"log"

	"novastream/models"
	"novastream/services/playback"
)

// PrepareSubtitleTracks converts SubtitleStreamInfo (from track_helper.go) to SubtitleTrackInfo
// (from subtitle_extract.go) format for extraction.
// This is used by playback handler when probing video metadata.
func PrepareSubtitleTracks(streams []SubtitleStreamInfo) []SubtitleTrackInfo {
	tracks := make([]SubtitleTrackInfo, len(streams))
	for i, s := range streams {
		tracks[i] = SubtitleTrackInfo{
			Index:         i,         // Relative index for frontend track selection
			AbsoluteIndex: s.Index,   // Absolute ffprobe stream index for ffmpeg -map
			Language:      s.Language,
			Title:         s.Title,
			Codec:         s.Codec,
			Forced:        s.IsForced,
		}
	}
	return tracks
}

// ConvertPlaybackTracksToHandler converts playback.SubtitleTrackInfo to handlers.SubtitleTrackInfo.
// This is used by prequeue handler which stores tracks in the playback package format.
func ConvertPlaybackTracksToHandler(tracks []playback.SubtitleTrackInfo) []SubtitleTrackInfo {
	result := make([]SubtitleTrackInfo, len(tracks))
	for i, t := range tracks {
		result[i] = SubtitleTrackInfo{
			Index:         t.Index,
			AbsoluteIndex: t.AbsoluteIndex,
			Language:      t.Language,
			Title:         t.Title,
			Codec:         t.Codec,
			Forced:        t.Forced,
		}
	}
	return result
}

// StartSubtitleExtraction starts extraction and returns session info map.
// This consolidates the common pattern from playback.go and prequeue.go:
// 1. Convert stream metadata to track info
// 2. Start pre-extraction
// 3. Convert extraction sessions to session info for the frontend
func StartSubtitleExtraction(
	ctx context.Context,
	extractor SubtitlePreExtractor,
	path string,
	streams []SubtitleStreamInfo,
	startOffset float64,
) map[int]*models.SubtitleSessionInfo {
	if extractor == nil || len(streams) == 0 {
		return nil
	}

	// Convert streams to track info
	tracks := PrepareSubtitleTracks(streams)

	log.Printf("[subtitle-helper] Starting subtitle extraction for %d tracks at offset %.3f", len(tracks), startOffset)

	// Start extraction
	sessions := extractor.StartPreExtraction(ctx, path, tracks, startOffset)

	// Convert sessions to SubtitleSessionInfo for the frontend
	return ConvertSessionsToInfo(sessions, streams)
}

// ConvertSessionsToInfo converts extraction sessions to SubtitleSessionInfo.
// This handles the common session-to-info conversion with proper sync locking.
func ConvertSessionsToInfo(
	sessions map[int]*SubtitleExtractSession,
	streams []SubtitleStreamInfo,
) map[int]*models.SubtitleSessionInfo {
	if sessions == nil {
		return nil
	}

	sessionInfos := make(map[int]*models.SubtitleSessionInfo)
	for relativeIdx, session := range sessions {
		if relativeIdx < 0 || relativeIdx >= len(streams) {
			continue
		}
		stream := streams[relativeIdx]

		// Get first cue time for subtitle sync (requires lock)
		session.mu.Lock()
		firstCueTime := session.FirstCueTime
		session.mu.Unlock()

		sessionInfos[relativeIdx] = &models.SubtitleSessionInfo{
			SessionID:    session.ID,
			VTTUrl:       "/api/video/subtitles/" + session.ID + "/subtitles.vtt",
			TrackIndex:   relativeIdx,
			Language:     stream.Language,
			Title:        stream.Title,
			Codec:        stream.Codec,
			IsForced:     stream.IsForced,
			IsExtracting: !session.IsExtractionComplete(),
			FirstCueTime: firstCueTime,
		}
	}

	log.Printf("[subtitle-helper] Converted %d subtitle sessions", len(sessionInfos))
	return sessionInfos
}

// ConvertSessionsFromPlaybackTracks converts extraction sessions to SubtitleSessionInfo
// using playback.SubtitleTrackInfo as the source metadata.
// This is used by prequeue handler which stores track info in the playback package format.
func ConvertSessionsFromPlaybackTracks(
	sessions map[int]*SubtitleExtractSession,
	tracks []playback.SubtitleTrackInfo,
) map[int]*models.SubtitleSessionInfo {
	if sessions == nil {
		return nil
	}

	sessionInfos := make(map[int]*models.SubtitleSessionInfo)
	for relativeIdx, session := range sessions {
		if relativeIdx < 0 || relativeIdx >= len(tracks) {
			continue
		}
		track := tracks[relativeIdx]

		// Get first cue time for subtitle sync (requires lock)
		session.mu.Lock()
		firstCueTime := session.FirstCueTime
		session.mu.Unlock()

		sessionInfos[relativeIdx] = &models.SubtitleSessionInfo{
			SessionID:    session.ID,
			VTTUrl:       "/api/video/subtitles/" + session.ID + "/subtitles.vtt",
			TrackIndex:   relativeIdx,
			Language:     track.Language,
			Title:        track.Title,
			Codec:        track.Codec,
			IsForced:     track.Forced,
			IsExtracting: !session.IsExtractionComplete(),
			FirstCueTime: firstCueTime,
		}
	}

	log.Printf("[subtitle-helper] Converted %d subtitle sessions from playback tracks", len(sessionInfos))
	return sessionInfos
}
