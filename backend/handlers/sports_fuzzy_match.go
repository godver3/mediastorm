package handlers

import "strings"

// At most one insertion, deletion or substitution, with bounded work per token.
func sportsOneEdit(a, b string) bool {
	x, y := []rune(a), []rune(b)
	if len(x) < 5 || len(y) < 4 || len(x) > 64 || len(y) > 65 || len(x)-len(y) > 1 || len(y)-len(x) > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < len(x) && j < len(y) {
		if x[i] == y[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		if len(x) >= len(y) {
			i++
		}
		if len(y) >= len(x) {
			j++
		}
	}
	if i < len(x) || j < len(y) {
		edits++
	}
	return edits == 1
}

func sportsFuzzySide(side []string, team sportsTeamIdentity) (bool, int) {
	nickname := sportsTokens(team.nickname)
	if len(nickname) == 0 || len(side) > 24 {
		return false, 0
	}
	used := make([]bool, len(side))
	edits := 0
	for _, want := range nickname {
		match := -1
		for i, token := range side {
			if !used[i] && token == want {
				match = i
				break
			}
		}
		if match < 0 {
			for i, token := range side {
				if !used[i] && sportsOneEdit(want, token) {
					if match >= 0 {
						return false, 0
					}
					match = i
				}
			}
			if match >= 0 {
				edits++
			}
		}
		if match < 0 || edits > 1 {
			return false, 0
		}
		used[match] = true
	}
	allowed := make(map[string]bool)
	for _, token := range sportsTokens(team.name + " " + team.location + " " + team.abbreviation) {
		allowed[token] = true
	}
	for i, token := range side {
		if used[i] || allowed[token] {
			continue
		}
		if _, noise := sportsNoiseTokens[token]; noise {
			continue
		}
		return false, 0 // Do not explain away an explicit different opponent/provider word.
	}
	return true, edits
}

func scoreFuzzySportsMatchup(target string, home, away sportsTeamIdentity) sportsEvidence {
	if !containsMatchupMarker(target) || hasNonLiveSportsLabel(target) {
		return sportsEvidence{}
	}
	tokens := sportsTokens(strings.ReplaceAll(target, "@", " vs "))
	split := -1
	for i, token := range tokens {
		if token == "vs" || token == "versus" || token == "at" {
			if split >= 0 {
				return sportsEvidence{}
			}
			split = i
		}
	}
	if split <= 0 || split >= len(tokens)-1 {
		return sportsEvidence{}
	}
	matches := 0
	for _, pair := range [][2]sportsTeamIdentity{{home, away}, {away, home}} {
		left, le := sportsFuzzySide(tokens[:split], pair[0])
		right, re := sportsFuzzySide(tokens[split+1:], pair[1])
		if left && right && le+re == 1 {
			matches++
		}
	}
	if matches != 1 {
		return sportsEvidence{}
	}
	return sportsEvidence{score: .70, reason: "Possible matchup: one team-name spelling difference", on: "matchup-name", terms: append(sportsTokens(home.nickname), sportsTokens(away.nickname)...)}
}
