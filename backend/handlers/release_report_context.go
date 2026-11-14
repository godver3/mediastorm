package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/debrid"
	"novastream/services/indexer"
	"novastream/services/trackselect"
	"novastream/utils/language"
)

func reportContext(resolver *trackselect.Resolver, profile, client, title string, episode *models.EpisodeReference) badstreams.PlaybackContext {
	ctx := badstreams.PlaybackContext{ProfileID: profile, ClientID: client, TitleID: title, Episode: episode}
	if resolver != nil {
		ctx.AudioLanguage = resolver.Resolve(profile, client, title).AudioLanguage
	}
	return ctx
}

func reportContextForResult(resolver *trackselect.Resolver, result models.NZBResult, profile, client, title string) badstreams.PlaybackContext {
	if profile == "" {
		profile = result.Attributes["profileId"]
	}
	if title == "" {
		title = result.Attributes["titleId"]
	}
	var episode *models.EpisodeReference
	if number, _ := strconv.Atoi(result.Attributes["targetEpisode"]); number > 0 {
		season, _ := strconv.Atoi(result.Attributes["targetSeason"])
		episode = &models.EpisodeReference{SeasonNumber: season, EpisodeNumber: number}
		if raw := result.Attributes["targetEpisodeNumbering"]; raw != "" {
			var numbering models.EpisodeNumbering
			if json.Unmarshal([]byte(raw), &numbering) == nil {
				episode.Numbering = &numbering
			}
		}
	}
	return reportContext(resolver, profile, client, title, episode)
}

func reportContextForSearch(resolver *trackselect.Resolver, opts indexer.SearchOptions) badstreams.PlaybackContext {
	parsed := debrid.ParseQuery(opts.Query)
	var episode *models.EpisodeReference
	if opts.MediaType != "movie" && parsed.Episode > 0 {
		episode = &models.EpisodeReference{SeasonNumber: parsed.Season, EpisodeNumber: parsed.Episode, Numbering: opts.Numbering}
	}
	return reportContext(resolver, opts.UserID, opts.ClientID, opts.TitleID, episode)
}

func (h *PrequeueHandler) reportContextForPrequeue(id, user, client string, episode *models.EpisodeReference) badstreams.PlaybackContext {
	title := ""
	if h.store != nil {
		if entry, ok := h.store.Get(id); ok {
			title = entry.TitleID
			if episode == nil {
				episode = entry.TargetEpisode
			}
			if user == "" {
				user = entry.UserID
			}
		}
	}
	return reportContext(h.trackPreferenceResolver(), user, client, title, episode)
}

// A track report removes only the reported stream index. The requested language
// must not silently change when its last usable track is removed.
func filterReportedAudio(service *badstreams.Service, result models.NZBResult, ctx badstreams.PlaybackContext, streams []models.AudioStreamInfo) ([]models.AudioStreamInfo, bool) {
	if service == nil {
		return streams, false
	}
	filtered := make([]models.AudioStreamInfo, 0, len(streams))
	removed := false
	for _, stream := range streams {
		index := stream.Index
		ctx.AudioTrackIndex = &index
		if service.MatchFor(result, ctx) != nil {
			removed = true
			continue
		}
		filtered = append(filtered, stream)
	}
	if !removed {
		return streams, false
	}
	if strings.TrimSpace(ctx.AudioLanguage) != "" {
		for _, stream := range filtered {
			if language.HasPreferredLanguage(stream.Language, ctx.AudioLanguage) {
				return filtered, false
			}
		}
		return filtered, true
	}
	return filtered, len(filtered) == 0
}

// Manual resolution must use the same healthy-track choice as automatic playback.
func (h *PlaybackHandler) filterResolvedReportedAudio(r *http.Request, result models.NZBResult, resolution *models.PlaybackResolution, ctx badstreams.PlaybackContext) (*models.PlaybackResolution, error) {
	if resolution == nil || !h.BadStreams.HasAudioReportFor(result, ctx) {
		return resolution, nil
	}
	probe := resolution.Probe
	if probe == nil && h.VideoProber != nil {
		probeCtx, cancel := context.WithTimeout(r.Context(), trackSelectionProbeTimeout)
		defer cancel()
		var err error
		probe, err = h.VideoProber.ProbeVideoFull(probeCtx, resolution.WebDAVPath)
		if err != nil {
			return nil, errors.New("could not verify reported audio tracks; choose another source")
		}
	}
	if probe == nil {
		return nil, errors.New("reported audio tracks require a verified alternative")
	}
	audio, unusable := filterReportedAudio(h.BadStreams, result, ctx, probe.AudioStreams)
	if unusable {
		return nil, errors.New("no healthy audio track in the requested language")
	}
	selection := trackselect.Select(audio, probe.SubtitleStreams, h.TrackPreferences.Resolve(ctx.ProfileID, ctx.ClientID, ctx.TitleID))
	if selection.Audio == nil && len(audio) > 0 && ctx.AudioLanguage == "" && len(h.TrackPreferences.Resolve(ctx.ProfileID, ctx.ClientID, ctx.TitleID).AllowedLanguages) == 0 {
		selection.Audio = trackselect.DescribeAudioTrack(audio, audio[0].Index)
	}
	selection.Audio = trackselect.DescribeAudioTrack(probe.AudioStreams, selection.AudioStreamIndex())
	copy := *resolution
	copy.TrackSelection = selection
	return &copy, nil
}

func annotateReportEpisode(attrs map[string]string, opts indexer.SearchOptions) {
	episode := reportContextForSearch(nil, opts).Episode
	if attrs == nil || episode == nil {
		return
	}
	attrs["targetSeason"] = strconv.Itoa(episode.SeasonNumber)
	attrs["targetEpisode"] = strconv.Itoa(episode.EpisodeNumber)
	if episode.Numbering != nil {
		encoded, _ := json.Marshal(episode.Numbering)
		attrs["targetEpisodeNumbering"] = string(encoded)
	}
}
