package debrid

import (
	"fmt"
	"strconv"
	"strings"

	"novastream/internal/mediaresolve"
	"novastream/models"
)

func buildSelectionHints(candidate models.NZBResult, directory string) mediaresolve.SelectionHints {
	hints := mediaresolve.SelectionHints{
		ReleaseTitle: candidate.Title,
		QueueName:    candidate.GUID,
		Directory:    directory,
	}

	attrs := candidate.Attributes
	if attrs == nil {
		fmt.Printf("[selection-hints] WARNING: candidate.Attributes is nil for %q\n", candidate.Title)
		return hints
	}

	// Debug: show all attributes
	fmt.Printf("[selection-hints] Building hints for %q with attributes:\n", candidate.Title)
	for k, v := range attrs {
		fmt.Printf("[selection-hints]   %s = %q\n", k, v)
	}

	if code := strings.TrimSpace(attrs["targetEpisodeCode"]); code != "" {
		hints.TargetEpisodeCode = code
	}

	if season := parsePositiveInt(attrs["targetSeason"]); season > 0 {
		hints.TargetSeason = season
	}
	if episode := parsePositiveInt(attrs["targetEpisode"]); episode > 0 {
		hints.TargetEpisode = episode
	}
	if absEpisode := parsePositiveInt(attrs["absoluteEpisodeNumber"]); absEpisode > 0 {
		hints.AbsoluteEpisodeNumber = absEpisode
	}

	if hints.TargetEpisodeCode == "" && hints.TargetSeason > 0 && hints.TargetEpisode > 0 {
		hints.TargetEpisodeCode = fmt.Sprintf("S%02dE%02d", hints.TargetSeason, hints.TargetEpisode)
	}

	if name := strings.TrimSpace(attrs["titleName"]); name != "" && hints.ReleaseTitle == "" {
		hints.ReleaseTitle = name
	}

	// Debug logging
	fmt.Printf("[selection-hints] Final hints: Season=%d, Episode=%d, AbsoluteEp=%d, Code=%q, ReleaseTitle=%q\n",
		hints.TargetSeason, hints.TargetEpisode, hints.AbsoluteEpisodeNumber, hints.TargetEpisodeCode, hints.ReleaseTitle)

	return hints
}

func parsePositiveInt(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
