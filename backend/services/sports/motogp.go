package sports

import (
	"context"
	"fmt"
	"novastream/models"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const motoGPBase = "https://api.motogp.pulselive.com/motogp/v1"

var motoGPUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type motoGPSeason struct {
	ID   string `json:"id"`
	Year int    `json:"year"`
}
type motoGPCategory struct {
	ID       string `json:"id"`
	LegacyID int    `json:"legacy_id"`
}
type motoGPEvent struct {
	ID           string `json:"id"`
	Name         string `json:"sponsored_name"`
	FallbackName string `json:"name"`
	Test         bool   `json:"test"`
	Circuit      struct {
		Name string `json:"name"`
	} `json:"circuit"`
}
type motoGPSession struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Number *int   `json:"number"`
	Date   string `json:"date"`
	Status string `json:"status"`
	Event  struct {
		ID string `json:"id"`
	} `json:"event"`
}
type motoGPClassification struct {
	Classification []struct {
		Position *int `json:"position"`
		Rider    struct {
			ID     string `json:"id"`
			Name   string `json:"full_name"`
			Number *int   `json:"number"`
		} `json:"rider"`
		Team struct {
			Name string `json:"name"`
		} `json:"team"`
		TotalLaps *int   `json:"total_laps"`
		Time      string `json:"time"`
		Status    string `json:"status"`
		BestLap   struct {
			Time string `json:"time"`
		} `json:"best_lap"`
		Gap struct {
			First string `json:"first"`
			Lap   string `json:"lap"`
		} `json:"gap"`
		Points *float64 `json:"points"`
	} `json:"classification"`
}

func motoGPStatus(raw string) (models.SportsGameStatus, bool) {
	switch strings.ToUpper(raw) {
	case "NOT-STARTED":
		return models.SportsGameScheduled, true
	case "FINISHED", "OFFICIAL":
		return models.SportsGameFinal, true
	// A timing payload merely being reachable must never imply an active session.
	default:
		return "", false
	}
}
func motoGPCancelled(raw string) bool {
	return strings.EqualFold(raw, "CANCELLED") || strings.EqualFold(raw, "CANCELED")
}

func normalizeMotoGPSessions(event motoGPEvent, raw []motoGPSession, now time.Time) models.SportsEvent {
	title := event.Name
	if title == "" {
		title = event.FallbackName
	}
	parent := models.SportsEvent{ID: "motogp:" + event.ID, ProviderEventID: event.ID, Title: title, League: "motogp", Sport: "racing", EventKind: "race", VenueName: event.Circuit.Name, UpdatedAt: now, Participants: []models.SportsParticipant{}}
	for _, r := range raw {
		status, known := motoGPStatus(r.Status)
		if !motoGPUUID.MatchString(r.ID) || !known || parseESPNDate(r.Date).IsZero() {
			continue
		}
		label := map[string]string{"RAC": "Race", "SPR": "Sprint", "PR": "Practice", "WUP": "Warm-up", "FP": "FP", "Q": "Q"}[r.Type]
		if label == "" {
			label = r.Type
		}
		if r.Number != nil {
			label += strconv.Itoa(*r.Number)
		}
		parent.SubEvents = append(parent.SubEvents, models.SportsEvent{ID: parent.ID + ":" + r.ID, ProviderEventID: event.ID, SessionID: r.ID, SessionType: label, Title: label, League: "motogp", Sport: "racing", EventKind: "race-session", StartTime: parseESPNDate(r.Date), Status: status, StatusDetail: r.Status, VenueName: event.Circuit.Name, UpdatedAt: now, Participants: []models.SportsParticipant{}})
	}
	sort.SliceStable(parent.SubEvents, func(i, j int) bool { return parent.SubEvents[i].StartTime.Before(parent.SubEvents[j].StartTime) })
	if len(parent.SubEvents) > 0 {
		main := parent.SubEvents[len(parent.SubEvents)-1]
		for _, session := range parent.SubEvents {
			if strings.HasPrefix(session.SessionType, "Race") {
				main = session
			}
		}
		parent.StartTime = main.StartTime
		parent.Status = main.Status
		parent.StatusDetail = main.StatusDetail
	}
	return parent
}

func (s *Service) fetchMotoGPBoard(ctx context.Context) ([]models.SportsEvent, error) {
	var seasons []motoGPSeason
	if err := s.racingJSON(ctx, motoGPBase+"/results/seasons", &seasons); err != nil {
		return nil, err
	}
	seasonID := ""
	for _, season := range seasons {
		if season.Year == time.Now().UTC().Year() && motoGPUUID.MatchString(season.ID) {
			seasonID = season.ID
			break
		}
	}
	if seasonID == "" {
		return nil, fmt.Errorf("MotoGP current year unavailable")
	}
	var categories []motoGPCategory
	if err := s.racingJSON(ctx, motoGPBase+"/results/categories?seasonUuid="+seasonID, &categories); err != nil {
		return nil, err
	}
	categoryID := ""
	for _, c := range categories {
		if c.LegacyID == 3 && motoGPUUID.MatchString(c.ID) {
			categoryID = c.ID
			break
		}
	}
	if categoryID == "" {
		return nil, fmt.Errorf("MotoGP category unavailable")
	}
	var events []motoGPEvent
	if err := s.racingJSON(ctx, motoGPBase+"/results/events?seasonUuid="+seasonID, &events); err != nil {
		return nil, err
	}
	if len(events) == 0 || len(events) > 64 {
		return nil, fmt.Errorf("unexpected MotoGP calendar size")
	}
	result := make([]models.SportsEvent, len(events))
	failures := make([]error, len(events))
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, event := range events {
		if event.Test || !motoGPUUID.MatchString(event.ID) {
			continue
		}
		wg.Add(1)
		go func(i int, event motoGPEvent) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				failures[i] = ctx.Err()
				return
			}
			defer func() { <-slots }()
			var sessions []motoGPSession
			if err := s.racingJSON(ctx, motoGPBase+"/results/sessions?eventUuid="+event.ID+"&categoryUuid="+categoryID, &sessions); err != nil {
				failures[i] = err
				return
			}
			result[i] = normalizeMotoGPSessions(event, sessions, time.Now())
			// A cancelled session (e.g. weather) is dropped, not treated as an unknown lifecycle.
			expected := 0
			for _, session := range sessions {
				if !motoGPCancelled(session.Status) {
					expected++
				}
			}
			if len(sessions) == 0 || len(result[i].SubEvents) != expected {
				failures[i] = fmt.Errorf("MotoGP session schedule incomplete or lifecycle unsupported")
			}
		}(i, event)
	}
	wg.Wait()
	out := []models.SportsEvent{}
	for i, event := range result {
		if failures[i] != nil {
			return nil, failures[i]
		}
		if len(event.SubEvents) > 0 {
			out = append(out, event)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("MotoGP sessions unavailable")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartTime.Before(out[j].StartTime) })
	return out, nil
}

func applyMotoGPClassification(session *models.SportsEvent, raw motoGPClassification) {
	session.Participants = []models.SportsParticipant{}
	for _, row := range raw.Classification {
		if !motoGPUUID.MatchString(row.Rider.ID) || row.Rider.Name == "" {
			continue
		}
		p := models.SportsParticipant{ID: "motogp:" + row.Rider.ID, Name: row.Rider.Name, Team: row.Team.Name}
		if row.Rider.Number != nil {
			p.Number = strconv.Itoa(*row.Rider.Number)
		}
		if row.Position != nil && *row.Position > 0 && *row.Position <= 1000 {
			p.Position = *row.Position
		}
		p.Winner = p.Position == 1 && session.Status == models.SportsGameFinal
		// Preserve source classification codes; INSTND means classified, not a lap-time value.
		if row.Status != "INSTND" {
			p.Result = row.Status
		}
		add := func(name, label, value string) {
			if value != "" {
				p.Statistics = append(p.Statistics, models.SportsRaceStatistic{Name: name, Label: label, Value: value})
			}
		}
		if row.TotalLaps != nil {
			add("lapsCompleted", "Laps", strconv.Itoa(*row.TotalLaps))
		}
		add("totalTime", "Elapsed time", row.Time)
		add("fastestLap", "Best lap", row.BestLap.Time)
		if row.Gap.First != "0.000" {
			add("behindTime", "Time behind", row.Gap.First)
		}
		if row.Gap.Lap != "0" {
			add("behindLaps", "Laps behind", row.Gap.Lap)
		}
		if row.Points != nil {
			add("points", "Points", strconv.FormatFloat(*row.Points, 'f', -1, 64))
		}
		session.Participants = append(session.Participants, p)
	}
	// Source position is retained, including missing positions. Never infer rank from array order.
}

func (s *Service) getMotoGPSession(ctx context.Context, eventID, sessionID string) (models.SportsEvent, error) {
	if !motoGPUUID.MatchString(eventID) || !motoGPUUID.MatchString(sessionID) {
		return models.SportsEvent{}, fmt.Errorf("invalid MotoGP identity")
	}
	board := s.GetRaceBoard(ctx)
	var session models.SportsEvent
	for _, event := range board.Events {
		if event.League == "motogp" && event.ProviderEventID == eventID {
			for _, candidate := range event.SubEvents {
				if candidate.SessionID == sessionID {
					session = candidate
					session.Stale = event.Stale
				}
			}
		}
	}
	if session.ID == "" {
		return session, fmt.Errorf("MotoGP session not found in enabled calendar")
	}
	if session.Status == models.SportsGameScheduled {
		return session, nil
	}
	s.raceDetailMu.Lock()
	defer s.raceDetailMu.Unlock()
	if s.raceDetails == nil {
		s.raceDetails = map[string]raceSessionEntry{}
	}
	old, exists := s.raceDetails[session.ID]
	if exists && time.Now().Before(old.expires) {
		out := old.event
		out.Stale = out.Stale || session.Stale
		return out, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var raw motoGPClassification
	err := s.racingJSON(requestCtx, motoGPBase+"/results/session/"+sessionID+"/classification?seasonYear="+strconv.Itoa(session.StartTime.Year())+"&test=false", &raw)
	if err == nil && len(raw.Classification) > 0 {
		applyMotoGPClassification(&session, raw)
	}
	ttl := 10 * time.Minute
	if err != nil || len(session.Participants) == 0 {
		if exists {
			session = old.event
		}
		session.Stale = true
		ttl = time.Minute
	} else {
		session.UpdatedAt = time.Now()
	}
	if len(s.raceDetails) >= 32 {
		for key := range s.raceDetails {
			delete(s.raceDetails, key)
			break
		}
	}
	s.raceDetails[session.ID] = raceSessionEntry{event: session, expires: time.Now().Add(ttl)}
	return session, nil
}
