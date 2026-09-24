package sports

import (
	"encoding/json"
	"novastream/models"
	"strconv"
	"strings"
)

// Tennis groups matches beneath tournaments; MMA puts every bout in competitions.
// Keep their individual identities and start times instead of the tournament/card date.
func scoreboardEventGames(event espnEvent, league League) []models.SportsGame {
	if league.Sport != "tennis" && league.Sport != "mma" && league.Slug != "tgl" {
		if game, ok := espnEventToGame(event, league); ok {
			return []models.SportsGame{game}
		}
		return nil
	}
	competitions := append([]espnCompetition(nil), event.Competitions...)
	for _, group := range event.Groupings {
		competitions = append(competitions, group.Competitions...)
	}
	games := []models.SportsGame{}
	seen := map[string]bool{}
	for _, competition := range competitions {
		if competition.ID == "" || seen[competition.ID] || len(competition.Competitors) != 2 {
			continue
		}
		if league.Slug == "tgl" {
			// Athlete head-to-head holes belong inside the team match, not separate hub cards.
			if competition.Competitors[0].Athlete != nil || competition.Competitors[1].Athlete != nil {
				continue
			}
		}
		seen[competition.ID] = true
		// Unknown set results stay unknown; never invent a 0–0 result for a future match.
		if league.Sport == "tennis" && competition.Status.Type.State != "pre" {
			competition.Competitors = append([]espnCompetitor(nil), competition.Competitors...)
			for i := range competition.Competitors {
				c := &competition.Competitors[i]
				if espnScore(c.Score) != "" {
					continue
				}
				wins, known := 0, false
				for _, set := range c.Linescores {
					if set.Winner != nil {
						known = true
						if *set.Winner {
							wins++
						}
					}
				}
				if known {
					c.Score = json.RawMessage(strconv.Itoa(wins))
				}
			}
		}
		match := event
		match.ID = league.ID + ":" + event.ID + ":" + competition.ID
		match.Name = ""
		if competition.Date != "" {
			match.Date = competition.Date
		}
		match.Competitions = []espnCompetition{competition}
		if game, ok := espnEventToGame(match, league); ok {
			game.ParentEventID = event.ID
			game.ProviderEventID = competition.ID
			if league.Sport == "tennis" {
				parts := []string{}
				for _, value := range []string{event.Name, competition.Type.Text, competition.Round.DisplayName} {
					if value = strings.TrimSpace(value); value != "" {
						parts = append(parts, value)
					}
				}
				game.EventContext = strings.Join(parts, " · ")
				if competition.Status.Period > 0 {
					game.Period = "Set " + strconv.Itoa(competition.Status.Period)
				}
			}
			// Broadcasts often name the whole card/tournament, not each bout or
			// match. Keep that identity alongside the individual competitors.
			if parent := strings.TrimSpace(event.Name); parent != "" {
				game.Title = parent + ": " + game.Title
			}
			games = append(games, game)
		}
	}
	return games
}
