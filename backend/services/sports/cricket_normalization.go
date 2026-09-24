package sports

import (
	"bytes"
	"encoding/json"
	"fmt"
	"novastream/models"
	"regexp"
	"sort"
	"strconv"
)

// ESPN mixes JSON and string booleans. Reject malformed values instead of
// silently changing a corrupt winner field into a loss.
func parseESPNBoolean(raw json.RawMessage) (*bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var value bool
	switch string(raw) {
	case "true", `"true"`:
		value = true
	case "false", `"false"`:
		value = false
	default:
		return nil, fmt.Errorf("invalid boolean %s", raw)
	}
	return &value, nil
}

var cricketTargetPattern = regexp.MustCompile(`\btarget ([0-9]+)\b`)

func normalizeCricketInnings(comp espnCompetition) []models.SportsCricketInnings {
	var result []models.SportsCricketInnings
	for _, c := range comp.Competitors {
		for _, inn := range c.Linescores {
			// isBatting identifies whose innings this is, not who is batting now.
			// Opponent placeholders may contain overs and zeros, so do not infer ownership.
			if !inn.IsBatting || inn.Period < 1 {
				continue
			}
			row := models.SportsCricketInnings{TeamID: competitorTeam(c).ID, Number: inn.Period, Runs: inn.Runs, Wickets: inn.Wickets, Overs: espnScore(inn.Overs), Description: inn.Description, Target: inn.Target}
			if row.Runs != nil {
				row.Score = strconv.Itoa(*row.Runs)
				if row.Wickets != nil {
					row.Score += fmt.Sprintf("/%d", *row.Wickets)
				}
			}
			// isCurrent is explicitly 0/1 in cricket scoreboards. Never infer it from order.
			switch string(bytes.TrimSpace(inn.IsCurrent)) {
			case "0", "false", `"false"`:
				v := false
				row.Batting = &v
			case "1", "true", `"true"`:
				v := comp.Status.Type.State == "in"
				row.Batting = &v
			}
			// The aggregate display score's target belongs only to the current innings.
			if row.Target == nil && (string(inn.IsCurrent) == "1" || (row.Batting != nil && *row.Batting)) {
				if match := cricketTargetPattern.FindStringSubmatch(espnScore(c.Score)); len(match) == 2 {
					n, err := strconv.Atoi(match[1])
					if err == nil {
						row.Target = &n
					}
				}
			}
			result = append(result, row)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result
}
