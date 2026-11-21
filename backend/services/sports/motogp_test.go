package sports

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"novastream/models"
	"os"
	"strings"
	"testing"
	"time"
)

func motoFixture(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile("testdata/motogp-" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
func TestMotoGPCapturedScheduleAndResults(t *testing.T) {
	var sessions []motoGPSession
	motoFixture(t, "sessions", &sessions)
	raw := motoGPEvent{ID: "23b7a561-17e4-4aa6-9be3-2529a5b69938", Name: "Grand Prix"}
	event := normalizeMotoGPSessions(raw, sessions, time.Now())
	if len(event.SubEvents) != 8 || event.Status != models.SportsGameFinal {
		t.Fatalf("weekend lost: %+v", event)
	}
	race := event.SubEvents[7]
	if race.SessionType != "Race" || event.StartTime != race.StartTime || race.ID == event.ID {
		t.Fatal("weekend/session mismatch")
	}
	var classification motoGPClassification
	motoFixture(t, "classification", &classification)
	applyMotoGPClassification(&race, classification)
	if len(race.Participants) != 22 || race.Participants[0].Position != 1 || race.Participants[0].Number != "93" || race.Participants[0].Team != "Ducati Lenovo Team" {
		t.Fatal("classification fields lost")
	}
	classification.Classification[0].Position = nil
	applyMotoGPClassification(&race, classification)
	if race.Participants[0].Position != 0 || race.Participants[0].Winner {
		t.Fatal("invented rank from source order")
	}
	motoFixture(t, "future-sessions", &sessions)
	event = normalizeMotoGPSessions(raw, sessions, time.Now().AddDate(20, 0, 0))
	if len(event.SubEvents) != 8 || event.Status != models.SportsGameScheduled {
		t.Fatal("date inferred final status")
	}
	sessions[0].Status = "UNRECOGNIZED"
	event = normalizeMotoGPSessions(raw, sessions, time.Now())
	if len(event.SubEvents) != 7 {
		t.Fatal("unknown lifecycle fabricated")
	}
}
func TestMotoGPDetailRetainsResultsAndValidatesIdentity(t *testing.T) {
	var sessions []motoGPSession
	motoFixture(t, "sessions", &sessions)
	event := normalizeMotoGPSessions(motoGPEvent{ID: "23b7a561-17e4-4aa6-9be3-2529a5b69938", Name: "Grand Prix"}, sessions, time.Now())
	s := NewService(t.TempDir())
	s.SetEnabledLeagueIDs([]string{"motogp"})
	s.raceBoards = map[string]raceBoardEntry{"motogp": {events: []models.SportsEvent{event}, updated: time.Now(), expires: time.Now().Add(time.Hour)}}
	data, err := os.ReadFile("testdata/motogp-classification.json")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fail := false
	s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if fail {
			return nil, fmt.Errorf("offline")
		}
		if r.URL.Host != "api.motogp.pulselive.com" {
			t.Fatal("wrong host")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}
	race := event.SubEvents[7]
	first, err := s.GetRaceSession(context.Background(), "motogp", event.ProviderEventID, race.SessionID)
	if err != nil || len(first.Participants) != 22 {
		t.Fatal(err, "missing results")
	}
	old := s.raceDetails[race.ID]
	old.expires = time.Now().Add(-time.Second)
	s.raceDetails[race.ID] = old
	fail = true
	stale, err := s.GetRaceSession(context.Background(), "motogp", event.ProviderEventID, race.SessionID)
	if err != nil || !stale.Stale || len(stale.Participants) != 22 {
		t.Fatal("last good results lost")
	}
	s.GetRaceSession(context.Background(), "motogp", event.ProviderEventID, race.SessionID)
	if calls != 2 {
		t.Fatal("failure backoff missing", calls)
	}
	if _, err = s.GetRaceSession(context.Background(), "motogp", "../../x", race.SessionID); err == nil {
		t.Fatal("unsafe identity accepted")
	}
	if calls != 2 {
		t.Fatal("invalid identity requested provider")
	}
	s.SetEnabledLeagueIDs([]string{"nba"})
	if _, err = s.GetRaceSession(context.Background(), "motogp", event.ProviderEventID, race.SessionID); err == nil {
		t.Fatal("disabled league exposed")
	}
}

func TestMotoGPBoardUsesFirstPartyAndRetainsCalendar(t *testing.T) {
	s := NewService(t.TempDir())
	s.SetEnabledLeagueIDs([]string{"motogp"})
	sessions, err := os.ReadFile("testdata/motogp-sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fail := false
	s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if fail {
			return nil, fmt.Errorf("offline")
		}
		if r.URL.Host != "api.motogp.pulselive.com" {
			t.Fatal("unexpected provider")
		}
		body := ""
		switch r.URL.Path {
		case "/motogp/v1/results/seasons":
			body = fmt.Sprintf(`[{"id":"e88b4e43-2209-47aa-8e83-0e0b1cedde6e","year":%d}]`, time.Now().UTC().Year())
		case "/motogp/v1/results/categories":
			body = `[{"id":"e8c110ad-64aa-4e8e-8a86-f2f152f6a942","legacy_id":3}]`
		case "/motogp/v1/results/events":
			body = `[{"id":"23b7a561-17e4-4aa6-9be3-2529a5b69938","sponsored_name":"Race"},{"id":"23b7a561-17e4-4aa6-9be3-2529a5b69939","test":true}]`
		case "/motogp/v1/results/sessions":
			body = string(sessions)
		default:
			return nil, fmt.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	board := s.GetRaceBoard(context.Background())
	if len(board.Events) != 1 || len(board.Events[0].SubEvents) != 8 || calls != 4 {
		t.Fatalf("board/test filter invalid %+v calls=%d", board, calls)
	}
	s.GetRaceBoard(context.Background())
	if calls != 4 {
		t.Fatal("board cache missed")
	}
	cached := s.raceBoards["motogp"]
	cached.expires = time.Now().Add(-time.Second)
	s.raceBoards["motogp"] = cached
	fail = true
	board = s.GetRaceBoard(context.Background())
	if len(board.Events) != 1 || !board.Events[0].Stale || board.Leagues[0].Unavailable {
		t.Fatal("last good board lost")
	}
	s.GetRaceBoard(context.Background())
	if calls != 5 {
		t.Fatal("failure backoff missing")
	}
}

func TestMotoGPRestartDefinesWeekendMainRace(t *testing.T) {
	var sessions []motoGPSession
	motoFixture(t, "sessions", &sessions)
	restart := sessions[len(sessions)-1]
	number := 2
	restart.Number = &number
	restart.Date = "2026-08-30T15:17:00+00:00"
	restart.ID = "0c6f6ec2-1f96-4b95-b634-54d01a5bf1d3"
	sessions = append(sessions, restart)
	event := normalizeMotoGPSessions(motoGPEvent{ID: "23b7a561-17e4-4aa6-9be3-2529a5b69938", Name: "Grand Prix"}, sessions, time.Now())
	if !event.StartTime.Equal(parseESPNDate(restart.Date)) || event.SubEvents[len(event.SubEvents)-1].SessionType != "Race2" {
		t.Fatal("race restart lost")
	}
}

// A weather-cancelled session (Australia 2026) must not fail the whole MotoGP calendar.
func TestMotoGPBoardToleratesCancelledSessions(t *testing.T) {
	const (
		season   = "11111111-1111-4111-8111-111111111111"
		category = "22222222-2222-4222-8222-222222222222"
		event    = "33333333-3333-4333-8333-333333333333"
	)
	year := time.Now().UTC().Year()
	bodies := map[string]string{
		"/motogp/v1/results/seasons":    fmt.Sprintf(`[{"id":%q,"year":%d}]`, season, year),
		"/motogp/v1/results/categories": fmt.Sprintf(`[{"id":%q,"legacy_id":3}]`, category),
		"/motogp/v1/results/events":     fmt.Sprintf(`[{"id":%q,"name":"Grand Prix of Australia"}]`, event),
		"/motogp/v1/results/sessions": `[
			{"id":"44444444-4444-4444-8444-444444444441","type":"RAC","date":"2026-10-25T05:00:00+00:00","status":"FINISHED"},
			{"id":"44444444-4444-4444-8444-444444444442","type":"SPR","date":"2026-10-24T05:00:00+00:00","status":"CANCELLED"}]`,
	}
	s := NewService(t.TempDir())
	s.client = &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	events, err := s.fetchMotoGPBoard(context.Background())
	if err != nil {
		t.Fatalf("cancelled session failed the board: %v", err)
	}
	if len(events) != 1 || len(events[0].SubEvents) != 1 || events[0].SubEvents[0].SessionType != "Race" {
		t.Fatalf("unexpected board: %+v", events)
	}
}
