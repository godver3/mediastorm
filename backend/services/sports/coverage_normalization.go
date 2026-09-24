package sports

import (
	"errors"
	"fmt"
	"novastream/models"
	"strings"
	"time"
)

// New team sports use literal provider period/set values, without NFL drives,
// MLB runner diagrams or guesses about regulation period counts.
func applyGenericScoreboardPeriods(game *models.SportsGame, comp espnCompetition, league League) {
	switch league.Sport {
	case "australian-football", "lacrosse", "volleyball", "field-hockey", "water-polo", "baseball", "hockey", "basketball", "football", "soccer":
	default:
		return
	}
	if !strings.HasPrefix(league.ID, "espn:") || game.Status == models.SportsGameScheduled {
		return
	}
	sides := map[string][]espnLineScore{}
	for _, c := range comp.Competitors {
		sides[competitorTeam(c).ID] = c.Linescores
	}
	home, away := sides[game.HomeTeam.ID], sides[game.AwayTeam.ID]
	if len(home) == 0 && len(away) == 0 {
		return
	}
	detail := game.Detail
	if detail == nil {
		detail = &models.SportsGameDetail{Source: "espn", UpdatedAt: time.Now()}
	}
	detail.Periods = nil
	for i := 0; i < max(len(home), len(away)); i++ {
		values := []string{"", ""}
		for side, rows := range [][]espnLineScore{away, home} {
			if i < len(rows) {
				values[side] = espnScore(rows[i].DisplayValue)
				if values[side] == "" {
					values[side] = espnScore(rows[i].Value)
				}
			}
		}
		if values[0] == "" && values[1] == "" {
			continue
		}
		label := periodLabel(i+1, league.Sport)
		if league.Sport == "volleyball" {
			label = fmt.Sprintf("Set %d", i+1)
		}
		detail.Periods = append(detail.Periods, models.SportsPeriodScore{Label: label, Away: values[0], Home: values[1]})
	}
	detail.Capabilities.Stats = len(detail.Periods) > 0
	game.Detail = detail
}

var errPartialScoreboard = errors.New("provider scoreboard may be truncated; displaying available events")

func mergeCoverageGames(previous, fresh []models.SportsGame) []models.SportsGame {
	result := append([]models.SportsGame(nil), fresh...)
	seen := map[string]bool{}
	for _, game := range fresh {
		seen[game.ID] = true
	}
	for _, game := range previous {
		if !seen[game.ID] {
			result = append(result, game)
			seen[game.ID] = true
		}
	}
	return result
}
