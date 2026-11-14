package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/localmedia"
	"novastream/services/sourceavailability"
	"novastream/services/trackselect"
)

type localSourceProbeContextKey struct{}

type localHintAccounts interface {
	Get(string) (models.Account, bool)
}
type localHintCatalogReader interface {
	GetItem(context.Context, string) (*models.LocalMediaItem, error)
}
type catalogueLocalChecker struct {
	playback *PrequeueHandler
	accounts localHintAccounts
}

// NewCatalogueLocalChecker shares playback authorization and probing without
// creating prequeue entries, media sessions, HLS buffers or downloads.
func NewCatalogueLocalChecker(playback *PrequeueHandler, accounts localHintAccounts) sourceavailability.LocalChecker {
	return &catalogueLocalChecker{playback, accounts}
}
func (c *catalogueLocalChecker) scope(profileID string) (prequeueLocalScope, bool) {
	profiles, ok := c.playback.users.(interface {
		Get(string) (models.User, bool)
	})
	if !ok || c.accounts == nil {
		return prequeueLocalScope{}, false
	}
	profile, ok := profiles.Get(profileID)
	if !ok || profile.AccountID == "" {
		return prequeueLocalScope{}, false
	}
	account, ok := c.accounts.Get(profile.AccountID)
	if !ok || account.IsExpired() {
		return prequeueLocalScope{}, false
	}
	return prequeueLocalScope{accountID: profile.AccountID, master: account.IsMaster}, true
}
func localHintEpisode(request sourceavailability.LocalRequest) *models.EpisodeReference {
	ep := request.Episode
	if ep == nil {
		return nil
	}
	return &models.EpisodeReference{Numbering: ep.Numbering, SeasonNumber: ep.SeasonNumber, EpisodeNumber: ep.EpisodeNumber, AbsoluteEpisodeNumber: ep.AbsoluteEpisodeNumber}
}
func localHintFingerprint(item models.LocalMediaItem) string {
	cert := ""
	if item.Metadata != nil {
		cert = item.Metadata.Certification
	}
	data, _ := json.Marshal([]any{item.ID, item.LibraryID, item.LibraryType, item.FilePath, item.FileName, item.MatchedTitleID, item.MatchStatus, item.SeasonNumber, item.EpisodeNumber, item.IsMissing, item.SizeBytes, item.ExternalIDs, cert})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (c *catalogueLocalChecker) find(ctx context.Context, request sourceavailability.LocalRequest) ([]models.LocalMediaItem, prequeueLocalScope, error) {
	h := c.playback
	scope, ok := c.scope(request.ProfileID)
	if !ok || h.localFallback == nil || h.localAccess == nil {
		return nil, prequeueLocalScope{}, errors.New("local source validation unavailable")
	}
	if request.Title.MediaType != "movie" && request.Episode == nil {
		return nil, prequeueLocalScope{}, errors.New("local series availability requires an episode")
	}
	if ep := request.Episode; ep != nil && (ep.Numbering == nil || ep.Numbering.Ordering != "" && ep.Numbering.Ordering != "official") {
		return nil, prequeueLocalScope{}, errors.New("local episode numbering unverified")
	}
	var accessErr error
	items, err := h.localFallback.FindPlaybackCandidates(ctx, models.LocalMediaMatchQuery{TitleID: request.Title.ID, IMDBID: request.Title.IMDBID, TMDBID: positiveIDString(request.Title.TMDBID), TVDBID: positiveIDString(request.Title.TVDBID), MediaType: request.Title.MediaType}, localHintEpisode(request), func(id string) bool {
		allowed, err := h.localAccess.CanAccess(ctx, id, scope.accountID, request.ProfileID, scope.master)
		if err != nil {
			accessErr = err
		}
		return err == nil && allowed
	})
	if err != nil || accessErr != nil {
		return nil, prequeueLocalScope{}, errors.New("local catalogue or authorization unavailable")
	}
	eligible := []models.LocalMediaItem{}
	for _, item := range items {
		if h.localItemAllowed(item, request.ProfileID) {
			eligible = append(eligible, item)
		}
	}
	return eligible, scope, nil
}

func (c *catalogueLocalChecker) fingerprint(item models.LocalMediaItem, request sourceavailability.LocalRequest) string {
	key, caps, filter := c.playback.prequeueSettingsScopeDetails(request.ProfileID, request.ClientID, request.Title.ID, nil)
	prefs := c.playback.trackPreferenceResolver().Resolve(request.ProfileID, request.ClientID, request.Title.ID)
	data, _ := json.Marshal([]any{localHintFingerprint(item), key, caps, filter, prefs, request.AudioLanguage})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (c *catalogueLocalChecker) Check(ctx context.Context, request sourceavailability.LocalRequest) (sourceavailability.LocalProof, error) {
	none := sourceavailability.LocalProof{}
	h := c.playback
	eligible, scope, err := c.find(ctx, request)
	if err != nil {
		return none, err
	}
	if len(eligible) == 0 {
		return none, nil
	}
	_, caps, filter := h.prequeueSettingsScopeDetails(request.ProfileID, request.ClientID, request.Title.ID, nil)
	prefs := h.trackPreferenceResolver().Resolve(request.ProfileID, request.ClientID, request.Title.ID)
	report := badstreams.PlaybackContext{ProfileID: request.ProfileID, ClientID: request.ClientID, TitleID: request.Title.ID, AudioLanguage: request.AudioLanguage, Episode: localHintEpisode(request)}
	for i, item := range eligible {
		if i >= 2 || ctx.Err() != nil {
			break
		}
		result := models.NZBResult{Title: item.FileName, Link: localmedia.BuildStreamPath(item), ServiceType: models.ContentServiceType("local"), Attributes: map[string]string{"localLibraryId": item.LibraryID, "profileId": request.ProfileID}}
		if h.badStreamsSvc.MatchFor(result, report) != nil {
			continue
		}
		resolution, err := h.resolveLocalItem(context.WithValue(ctx, localSourceProbeContextKey{}, true), item, prequeueResolutionOptions{mediaType: request.Title.MediaType, targetEpisode: localHintEpisode(request), userID: request.ProfileID, clientID: request.ClientID, localScope: &scope, adaptiveCaps: caps, localFilter: filter})
		if err != nil || resolution == nil || resolution.Probe == nil {
			continue
		}
		probe := resolution.Probe
		if validatePrequeueEpisodeDuration(request.Title.MediaType, localHintEpisode(request), probe.Duration) != nil {
			continue
		}
		if filter.HDRDVPolicy == models.HDRDVPolicyIncludeHDR && ValidateDVProfile(probe.DolbyVisionProfile, "hdr", probe.HasDolbyVision) != nil {
			continue
		}
		if rejected, _ := unknownTrackPolicyRejects(normalizeUnknownTrackPolicy(filter.UnknownTrackPolicy), probe.AudioStreams, probe.SubtitleStreams); rejected {
			continue
		}
		streams, unusable := filterReportedAudio(h.badStreamsSvc, result, report, probe.AudioStreams)
		if unusable {
			continue
		}
		filtered := []models.AudioStreamInfo{}
		for _, stream := range streams {
			if trackselect.IsCommentaryTrack(stream.Title) {
				continue
			}
			allowed := len(prefs.AllowedLanguages) == 0
			for _, lang := range prefs.AllowedLanguages {
				if trackselect.MatchesLanguage(stream.Language, stream.Title, lang) {
					allowed = true
				}
			}
			if allowed {
				filtered = append(filtered, stream)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		if request.AudioLanguage != "" && trackselect.FindAudioTrack(filtered, request.AudioLanguage) < 0 {
			continue
		}
		proof := sourceavailability.LocalProof{ItemID: item.ID, Fingerprint: c.fingerprint(item, request)}
		if c.Validate(ctx, request, proof) {
			return proof, nil
		}
	}
	return none, errors.New("matching local file or requested audio could not be verified")
}
func (c *catalogueLocalChecker) Validate(ctx context.Context, request sourceavailability.LocalRequest, proof sourceavailability.LocalProof) bool {
	if ctx.Err() != nil {
		return false
	}
	if proof.ItemID == "" {
		items, _, err := c.find(ctx, request)
		return ctx.Err() == nil && err == nil && len(items) == 0
	}
	scope, ok := c.scope(request.ProfileID)
	if !ok {
		return false
	}
	reader, ok := c.playback.localFallback.(localHintCatalogReader)
	if !ok {
		return false
	}
	item, err := reader.GetItem(ctx, proof.ItemID)
	if err != nil || item == nil || item.IsMissing || !c.playback.localItemAllowed(*item, request.ProfileID) || c.fingerprint(*item, request) != proof.Fingerprint {
		return false
	}
	allowed, err := c.playback.localAccess.CanAccess(ctx, item.LibraryID, scope.accountID, request.ProfileID, scope.master)
	return ctx.Err() == nil && err == nil && allowed
}
