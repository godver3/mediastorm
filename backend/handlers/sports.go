package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/models"
	"novastream/services/sports"
)

// alphaNumOnlyRegex strips everything but letters/digits so broadcast-name matching isn't
// fooled by punctuation/spacing differences between ESPN's broadcast label and a provider's
// channel naming (e.g. ESPN's "MLB.TV" vs a channel literally named "MLB TV" or "MLBTV" -
// a plain substring match on "mlb.tv" would miss both).
var alphaNumOnlyRegex = regexp.MustCompile(`[^a-z0-9]`)

func normalizeForMatch(s string) string {
	return alphaNumOnlyRegex.ReplaceAllString(strings.ToLower(s), "")
}

const sportsRefreshTimeout = 20 * time.Second

// maxSearchEPGChannels caps how many channel IDs SearchLiveHub will feed into
// GetScheduleMultiple - see the call site for why.
const maxSearchEPGChannels = 1500

// SportsHandler serves ESPN-backed scoreboard/schedule data and matches games to
// the caller's configured Live TV channels for "what can I watch this on" lookups.
type SportsHandler struct {
	service     *sports.Service
	liveHandler *LiveHandler
	epgService  LiveEPGNowPlayingProvider
	config      *config.Manager
}

type sportsScheduleProvider interface {
	GetScheduleMultiple(channelIDs []string, start, end time.Time) map[string][]models.EPGProgram
}

func (h *SportsHandler) SetConfigManager(manager *config.Manager) { h.config = manager }

// NewSportsHandler creates a new sports handler.
func NewSportsHandler(service *sports.Service, liveHandler *LiveHandler, epgService LiveEPGNowPlayingProvider) *SportsHandler {
	return &SportsHandler{service: service, liveHandler: liveHandler, epgService: epgService}
}

// GetScoreboard returns cached games, optionally filtered to one league (?league=mlb).
func (h *SportsHandler) GetScoreboard(w http.ResponseWriter, r *http.Request) {
	league := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("league")))
	if date := r.URL.Query().Get("date"); date != "" {
		if err := sports.ValidateScoreboardDate(date, time.Now()); err != nil {
			http.Error(w, `{"error":"date must be YYYY-MM-DD within the current schedule window"}`, http.StatusBadRequest)
			return
		}
		board, err := h.service.GetDatedScoreboard(r.Context(), date, league)
		if err != nil {
			http.Error(w, `{"error":"dated scoreboard unavailable"}`, http.StatusBadGateway)
			return
		}
		writeSportsJSON(w, board)
		return
	}
	games := h.service.GetScoreboard(league)
	if games == nil {
		games = []models.SportsGame{}
	}
	writeSportsJSON(w, map[string]any{"games": games})
}

// GetLeagues returns the tracked leagues.
func (h *SportsHandler) GetLeagues(w http.ResponseWriter, r *http.Request) {
	writeSportsJSON(w, map[string]any{"leagues": h.service.Leagues()})
}

// GetHub exposes generalized events grouped by lifecycle for large-screen clients.
func (h *SportsHandler) GetHub(w http.ResponseWriter, r *http.Request) {
	games := h.service.GetScoreboard(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("league"))))
	events := make([]models.SportsEvent, 0, len(games))
	for _, game := range games {
		participants := game.Participants
		if len(participants) == 0 {
			participants = []models.SportsParticipant{
				{ID: game.AwayTeam.ID, Name: game.AwayTeam.Name, Abbreviation: game.AwayTeam.Abbreviation, LogoURL: game.AwayTeam.LogoURL, Score: game.AwayTeam.Score, Winner: game.AwayTeam.Winner},
				{ID: game.HomeTeam.ID, Name: game.HomeTeam.Name, Abbreviation: game.HomeTeam.Abbreviation, LogoURL: game.HomeTeam.LogoURL, Score: game.HomeTeam.Score, Winner: game.HomeTeam.Winner},
			}
		}
		title := game.Title
		if title == "" {
			title = strings.TrimSpace(game.AwayTeam.Name + " vs " + game.HomeTeam.Name)
		}
		kind := game.EventKind
		if kind == "" {
			kind = "matchup"
		}
		events = append(events, models.SportsEvent{ID: game.ID, Title: title, League: game.League, Sport: game.Sport, EventKind: kind, StartTime: game.StartTime, Status: game.Status, StatusDetail: game.StatusDetail, Participants: participants, Broadcasts: game.Broadcasts, VenueName: game.VenueName})
	}
	writeSportsJSON(w, map[string]any{"events": events, "specialTournaments": []any{}})
}

// searchTerms splits a search query into individual lowercase words, dropping anything
// under 2 characters (single letters are too noisy to match on). "Chicago Cincinnati"
// becomes ["chicago", "cincinnati"].
func searchTerms(query string) []string {
	fields := strings.Fields(strings.ToLower(query))
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) >= 2 {
			terms = append(terms, field)
		}
	}
	return terms
}

// matchesAnySearchTerm reports whether target contains any one of terms as a substring.
func matchesAnySearchTerm(target string, terms []string) bool {
	lower := strings.ToLower(target)
	for _, term := range terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

// SearchLiveHub searches channel metadata, seven days of EPG, and cached sports events.
func (h *SportsHandler) SearchLiveHub(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) < 2 {
		http.Error(w, `{"error":"query must contain at least two characters"}`, http.StatusBadRequest)
		return
	}
	// Split into terms and match ANY of them (not the whole phrase as one substring) - a
	// caller like the Sports Hub's "Search" action passes both teams' names as one query
	// (e.g. "Chicago Cincinnati", ynotv-style), and no channel name literally contains that
	// exact phrase; OR-of-terms surfaces channels/programs naming either team instead of
	// requiring an exact multi-word match that would never hit.
	terms := searchTerms(query)
	channels, err := h.liveHandler.FetchFilteredChannelsForRequest(r)
	if err != nil {
		http.Error(w, `{"error":"failed to search live channels"}`, http.StatusBadGateway)
		return
	}
	channelMatches := make([]LiveChannel, 0)
	ids := make([]string, 0, len(channels))
	for _, channel := range channels {
		if channel.TvgID != "" {
			ids = append(ids, channel.TvgID)
		}
		if matchesAnySearchTerm(channel.Name+" "+channel.Group+" "+channel.TvgName, terms) && len(channelMatches) < 100 {
			channelMatches = append(channelMatches, channel)
		}
	}
	livePrograms := make([]models.EPGProgram, 0)
	upcomingPrograms := make([]models.EPGProgram, 0)
	// GetScheduleMultiple's per-miss fallback (findProgramsByChannelMatch) is now indexed
	// (see services/epg/service.go rebuildScheduleIndexLocked) instead of a linear scan per
	// call, which is what made this endpoint hang passing every channel's tvg-id on a large
	// playlist. Keep a generous cap anyway as defense in depth; above it, channel-name
	// matches (already computed) still return, just without the EPG-program-title pass.
	if schedule, ok := h.epgService.(sportsScheduleProvider); ok && len(ids) <= maxSearchEPGChannels {
		now := time.Now()
		schedules := schedule.GetScheduleMultiple(ids, now.Add(-time.Hour), now.Add(7*24*time.Hour))
		for _, programs := range schedules {
			for _, program := range programs {
				if !matchesAnySearchTerm(program.Title+" "+program.Description, terms) {
					continue
				}
				if !program.Start.After(now) && program.Stop.After(now) {
					if len(livePrograms) < 100 {
						livePrograms = append(livePrograms, program)
					}
				} else if len(upcomingPrograms) < 100 {
					upcomingPrograms = append(upcomingPrograms, program)
				}
			}
		}
	}
	sportsEvents := make([]models.SportsGame, 0)
	teams := make([]models.SportsTeam, 0)
	seenTeams := make(map[string]struct{})
	for _, game := range h.service.GetScoreboard("") {
		matchedEvent := matchesAnySearchTerm(game.Title+" "+game.HomeTeam.Name+" "+game.AwayTeam.Name+" "+game.League, terms)
		if matchedEvent && len(sportsEvents) < 100 {
			sportsEvents = append(sportsEvents, game)
		}
		for _, team := range []models.SportsTeam{game.HomeTeam, game.AwayTeam} {
			if !matchesAnySearchTerm(team.Name+" "+team.Location+" "+team.Nickname+" "+team.Abbreviation, terms) {
				continue
			}
			key := game.League + ":" + team.ID
			if _, exists := seenTeams[key]; exists {
				continue
			}
			seenTeams[key] = struct{}{}
			if len(teams) < 100 {
				teams = append(teams, team)
			}
		}
	}
	writeSportsJSON(w, map[string]any{"channels": channelMatches, "livePrograms": livePrograms, "upcomingPrograms": upcomingPrograms, "teams": teams, "sportsEvents": sportsEvents})
}

func (h *SportsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		http.Error(w, `{"error":"sports settings unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	settings, err := h.config.Load()
	if err != nil {
		http.Error(w, `{"error":"failed to load sports settings"}`, http.StatusInternalServerError)
		return
	}
	settings.Sports.Normalize()
	writeSportsJSON(w, settings.Sports)
}

func (h *SportsHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		http.Error(w, `{"error":"sports settings unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	var next config.SportsSettings
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&next); err != nil {
		http.Error(w, `{"error":"invalid sports settings"}`, http.StatusBadRequest)
		return
	}
	known := make(map[string]struct{})
	for _, league := range h.service.Leagues() {
		known[league.ID] = struct{}{}
	}
	clean := make([]string, 0, len(next.EnabledLeagues))
	seen := make(map[string]struct{})
	for _, raw := range next.EnabledLeagues {
		id := strings.ToLower(strings.TrimSpace(raw))
		if _, ok := known[id]; !ok {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	if len(clean) == 0 {
		http.Error(w, `{"error":"enable at least one supported league"}`, http.StatusBadRequest)
		return
	}
	next.EnabledLeagues = clean
	settings, err := h.config.Load()
	if err != nil {
		http.Error(w, `{"error":"failed to load settings"}`, http.StatusInternalServerError)
		return
	}
	settings.Sports = next
	if err := h.config.Save(settings); err != nil {
		http.Error(w, `{"error":"failed to save sports settings"}`, http.StatusInternalServerError)
		return
	}
	h.service.SetEnabledLeagueIDs(next.EnabledLeagues)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sportsRefreshTimeout)
		defer cancel()
		if err := h.service.Refresh(ctx); err != nil {
			log.Printf("[sports] settings refresh error: %v", err)
		}
	}()
	writeSportsJSON(w, next)
}

func (h *SportsHandler) ClearLogoCache(w http.ResponseWriter, r *http.Request) {
	if err := h.service.ClearLogoCache(); err != nil {
		http.Error(w, `{"error":"failed to clear sports logo cache"}`, http.StatusInternalServerError)
		return
	}
	writeSportsJSON(w, map[string]any{"status": "cleared"})
}

func (h *SportsHandler) GetLogo(w http.ResponseWriter, r *http.Request) {
	data, contentType, err := h.service.GetCachedLogo(r.Context(), strings.TrimSpace(r.URL.Query().Get("url")))
	if err != nil {
		http.Error(w, `{"error":"sports logo unavailable"}`, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	_, _ = w.Write(data)
}

// GetGame returns a single cached game by ID.
func (h *SportsHandler) GetGame(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		http.Error(w, `{"error":"missing game id"}`, http.StatusBadRequest)
		return
	}
	game, ok := h.service.GetGame(id)
	if !ok {
		http.Error(w, `{"error":"game not found"}`, http.StatusNotFound)
		return
	}
	writeSportsJSON(w, h.service.EnrichGame(r.Context(), game))
}

// GetStatus reports scoreboard cache health.
func (h *SportsHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	writeSportsJSON(w, h.service.GetStatus())
}

// Refresh triggers an immediate scoreboard refresh.
func (h *SportsHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sportsRefreshTimeout)
		defer cancel()
		if err := h.service.Refresh(ctx); err != nil {
			log.Printf("[sports] refresh error: %v", err)
		}
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"refresh started"}`))
}

// GetGameStreams finds candidate channels/streams for watching a game - "List Streams
// Here" support. Two modes, chosen by the caller (a specific broadcast badge vs. the main
// "List Streams Here" action - see components/sports/GameScoreCard.tsx):
//   - ?broadcast=<name>: scoped lookup, only channels whose name contains that one
//     broadcast/network name (e.g. tapping a "Peacock" badge).
//   - no broadcast param: searches the two team names (and EPG now-playing titles)
//     separately, since matching every channel against every broadcast name by default
//     was too broad (e.g. returned unrelated VOD-style channels that merely contain
//     "Peacock" in their name). Not authoritative; results are heuristic, not guaranteed.
func (h *SportsHandler) GetGameStreams(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		http.Error(w, `{"error":"missing game id"}`, http.StatusBadRequest)
		return
	}
	game, ok := h.service.GetGame(id)
	if !ok {
		http.Error(w, `{"error":"game not found"}`, http.StatusNotFound)
		return
	}

	if h.liveHandler == nil {
		writeSportsJSON(w, map[string]any{"streams": []models.SportsStreamMatch{}, "groups": []models.SportsStreamGroup{}})
		return
	}

	channels, err := h.liveHandler.FetchFilteredChannelsForRequest(r)
	if err != nil {
		log.Printf("[sports] GetGameStreams: failed to fetch live channels: %v", err)
		http.Error(w, `{"error":"failed to fetch live channels"}`, http.StatusBadGateway)
		return
	}
	// Broader search retains the profile/admin-filtered channels fetched above.
	if h.config != nil && r.URL.Query().Get("scope") != "all" {
		if settings, loadErr := h.config.Load(); loadErr == nil {
			sourceIDs := settings.Sports.DefaultSourceIDs
			categoryIDs := settings.Sports.DefaultCategoryIDs
			if override, ok := settings.Sports.LeagueSearchOverrides[game.League]; ok {
				if len(override.SourceIDs) > 0 {
					sourceIDs = override.SourceIDs
				}
				if len(override.CategoryIDs) > 0 {
					categoryIDs = override.CategoryIDs
				}
			}
			channels = filterSportsChannelsByScope(channels, sourceIDs, categoryIDs)
		}
	}

	broadcastFilter := strings.TrimSpace(r.URL.Query().Get("broadcast"))
	matches := matchGameToChannels(game, channels, h.epgService, broadcastFilter)
	matches = selectableSportsMatches(matches)
	streams, groups := buildSportsStreamGroups(matches)
	writeSportsJSON(w, map[string]any{"streams": streams, "groups": groups})
}

// Keep plausible candidates for explicit selection; a city or generic league is not enough.
func selectableSportsMatches(matches []models.SportsStreamMatch) []models.SportsStreamMatch {
	selected := make([]models.SportsStreamMatch, 0, len(matches))
	for _, match := range matches {
		if match.Confidence >= 0.65 && strings.TrimSpace(match.ChannelURL) != "" &&
			!hasNonLiveSportsLabel(match.ChannelName) && match.LifecycleState != sportsLifecycleEnded {
			selected = append(selected, match)
		}
	}
	return selected
}

func filterSportsChannelsByScope(channels []LiveChannel, sourceIDs, categoryIDs []string) []LiveChannel {
	if len(sourceIDs) == 0 && len(categoryIDs) == 0 {
		return channels
	}
	sources := make(map[string]struct{}, len(sourceIDs))
	categories := make(map[string]struct{}, len(categoryIDs))
	for _, id := range sourceIDs {
		sources[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	for _, id := range categoryIDs {
		categories[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	filtered := make([]LiveChannel, 0, len(channels))
	for _, channel := range channels {
		if len(sources) > 0 {
			if _, ok := sources[strings.ToLower(channel.SourceID)]; !ok {
				continue
			}
		}
		if len(categories) > 0 {
			if _, ok := categories[strings.ToLower(channel.Group)]; !ok {
				continue
			}
		}
		filtered = append(filtered, channel)
	}
	return filtered
}

// Options handles CORS preflight requests.
func (h *SportsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func writeSportsJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[sports] JSON encode error: %v", err)
	}
}

// matchGameToChannels scores every candidate through the same token-aware team/network
// matcher used by Manage Team Channels. Current and next EPG programs can promote a
// channel. Plausible candidates are returned for manual selection.
func matchGameToChannels(game models.SportsGame, channels []LiveChannel, epgService LiveEPGNowPlayingProvider, broadcastFilter string) []models.SportsStreamMatch {
	home := identityFromTeam(game.HomeTeam, game.League)
	away := identityFromTeam(game.AwayTeam, game.League)
	nowByID := make(map[string]models.EPGNowPlaying)
	if epgService != nil {
		ids := make([]string, 0, len(channels))
		for _, channel := range channels {
			if strings.TrimSpace(channel.TvgID) != "" {
				ids = append(ids, channel.TvgID)
			}
		}
		for _, now := range epgService.GetNowPlaying(ids) {
			nowByID[strings.ToLower(now.ChannelID)] = now
		}
	}

	matches := make([]models.SportsStreamMatch, 0)
	for _, channel := range channels {
		channelLifecycle := sportsLifecycle(channel.Name)
		if channelLifecycle == "" {
			channelLifecycle = sportsLifecycle(channel.TvgName)
		}
		if channelLifecycle == sportsLifecycleEnded || hasNonLiveSportsLabel(channel.Name) || hasNonLiveSportsLabel(channel.TvgName) {
			continue
		}
		var evidence sportsEvidence
		if broadcastFilter != "" {
			evidence = scoreBroadcastText(channel.Name, broadcastFilter)
			if evidence.score <= 0 && channel.TvgName != "" {
				evidence = scoreBroadcastText(channel.TvgName, broadcastFilter)
			}
			if evidence.score <= 0 {
				continue
			}
		} else {
			if game.EventKind != "" && game.EventKind != "matchup" {
				evidence = scoreSportsEventTitle(channel.Name, game.Title)
			} else {
				evidence = scoreMatchupText(channel.Name, home, away)
			}
			var alternate sportsEvidence
			if game.EventKind != "" && game.EventKind != "matchup" {
				alternate = scoreSportsEventTitle(channel.TvgName, game.Title)
			} else {
				alternate = scoreMatchupText(channel.TvgName, home, away)
			}
			if alternate.score > evidence.score {
				evidence = alternate
			}
		}

		if now, ok := nowByID[strings.ToLower(channel.TvgID)]; ok {
			for _, program := range []*models.EPGProgram{now.Current, now.Next} {
				if program == nil {
					continue
				}
				if hasNonLiveSportsLabel(program.Title) || hasNonLiveSportsLabel(program.Description) {
					continue
				}
				programEvidence := scoreMatchupText(program.Title, home, away)
				if game.EventKind != "" && game.EventKind != "matchup" {
					programEvidence = scoreSportsEventTitle(program.Title, game.Title)
				}
				fromDescription := false
				descriptionEvidence := scoreMatchupText(program.Description, home, away)
				if game.EventKind != "" && game.EventKind != "matchup" {
					descriptionEvidence = scoreSportsEventTitle(program.Description, game.Title)
				}
				if descriptionEvidence.score > programEvidence.score {
					programEvidence = descriptionEvidence
					fromDescription = true
				}
				if programEvidence.score <= 0 {
					continue
				}
				// Descriptions without schedule evidence are suggestions, not confirmed broadcasts.
				ceiling := 1.0
				if fromDescription {
					ceiling = 0.84
				}
				if !game.StartTime.IsZero() && !program.Start.IsZero() {
					distance := program.Start.Sub(game.StartTime)
					if distance < 0 {
						distance = -distance
					}
					if distance > 6*time.Hour {
						continue
					}
					if distance <= 2*time.Hour {
						ceiling = 1
						programEvidence.score = math.Min(ceiling, programEvidence.score+0.04)
					} else {
						ceiling = 0.84
					}
				}
				programEvidence.score = math.Min(ceiling, programEvidence.score)

				if broadcastFilter != "" {
					if programEvidence.score >= strongSportsConfidence {
						evidence.score = math.Max(evidence.score, 0.99)
						evidence.reason = broadcastFilter + " with both teams in EPG"
						evidence.terms = programEvidence.terms
						evidence.program = program.Title
						evidence.on = "epg-title"
					}
				} else if programEvidence.score > evidence.score {
					programEvidence.score = math.Min(ceiling, programEvidence.score+0.04)
					programEvidence.reason = "EPG: " + programEvidence.reason
					programEvidence.program = program.Title
					programEvidence.on = "epg-title"
					if fromDescription {
						programEvidence.on = "epg-description"
					}
					evidence = programEvidence
				}
			}
		}

		if evidence.score <= 0 {
			continue
		}
		score := roundSportsConfidence(evidence.score)
		matches = append(matches, models.SportsStreamMatch{
			ChannelID: channel.ID, ChannelName: channel.Name, ChannelURL: channel.URL,
			ChannelLogo: channel.Logo, ChannelTvgID: channel.TvgID, SourceID: channel.SourceID,
			SourceName: channel.SourceName, ProgramTitle: evidence.program,
			MatchedOn: evidence.on, Broadcast: broadcastFilter, MatchedTeam: evidence.team,
			Confidence: score, ConfidenceTier: confidenceTier(score), MatchReason: evidence.reason,
			MatchedTerms: evidence.terms, LifecycleState: channelLifecycle,
		})
	}
	sortSportsStreamMatches(matches)
	return matches
}

func scoreSportsEventTitle(value, title string) sportsEvidence {
	valueTokens := make(map[string]struct{})
	for _, token := range sportsTokens(value) {
		valueTokens[token] = struct{}{}
	}
	terms := make([]string, 0)
	for _, token := range sportsTokens(title) {
		if len(token) < 3 {
			continue
		}
		switch token {
		case "the", "and", "tour", "round", "event", "championship":
			continue
		}
		if _, ok := valueTokens[token]; ok {
			terms = append(terms, token)
		}
	}
	if len(terms) < 2 {
		return sportsEvidence{}
	}
	return sportsEvidence{score: math.Min(0.98, 0.82+float64(len(terms))*0.04), reason: "Event title match", terms: terms, on: "event-title"}
}

// Racing uses the existing generalized event contract, separate from matchup routes.
func (h *SportsHandler) GetRaces(w http.ResponseWriter, r *http.Request) {
	board := h.service.GetRaceBoard(r.Context())
	writeSportsJSON(w, board)
}
func (h *SportsHandler) GetRaceSession(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	event, err := h.service.GetRaceSession(r.Context(), vars["league"], vars["event"], vars["session"])
	if err != nil {
		http.Error(w, `{"error":"Race session unavailable"}`, http.StatusBadGateway)
		return
	}
	writeSportsJSON(w, event)
}

func (h *SportsHandler) GetCycling(w http.ResponseWriter, r *http.Request) {
	writeSportsJSON(w, h.service.GetCycling(r.Context()))
}
func (h *SportsHandler) GetCyclingStage(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	year, e1 := strconv.Atoi(vars["year"])
	stage, e2 := strconv.Atoi(vars["stage"])
	if e1 != nil || e2 != nil {
		http.Error(w, `{"error":"invalid cycling stage"}`, http.StatusBadRequest)
		return
	}
	detail, err := h.service.GetCyclingStage(r.Context(), vars["race"], year, stage)
	if err != nil {
		http.Error(w, `{"error":"cycling stage unavailable"}`, http.StatusBadGateway)
		return
	}
	writeSportsJSON(w, detail)
}

func (h *SportsHandler) GetMotoGPStandings(w http.ResponseWriter, r *http.Request) {
	writeSportsJSON(w, h.service.GetMotoGPEnrichment(r.Context(), ""))
}
func (h *SportsHandler) GetMotoGPCircuit(w http.ResponseWriter, r *http.Request) {
	writeSportsJSON(w, h.service.GetMotoGPEnrichment(r.Context(), mux.Vars(r)["event"]))
}

func (h *SportsHandler) GetLeagueStandings(w http.ResponseWriter, r *http.Request) {
	writeSportsJSON(w, h.service.GetLeagueStandings(r.Context(), mux.Vars(r)["league"]))
}

// GetF1Archive resolves an existing session before reading any external archive.
func (h *SportsHandler) GetF1Archive(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	event, err := h.service.GetRaceSession(r.Context(), "f1", vars["event"], vars["session"])
	if err != nil {
		http.Error(w, `{"error":"Race session unavailable"}`, http.StatusBadGateway)
		return
	}
	writeSportsJSON(w, h.service.GetF1Archive(r.Context(), event))
}
