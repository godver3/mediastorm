package handlers

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"novastream/services/debrid"
)

// Connectivity tests for non-Stremio sources also sample release names using
// their normal adapters. A failed sample does not negate a successful connection.
func sourceTestSearchResponse(message string, req TestScraperRequest, scrapers ...debrid.Scraper) map[string]interface{} {
	response := map[string]interface{}{"success": true, "message": message}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var results []debrid.ScrapeResult
	failed := false
	var firstErr error
	for _, scraper := range scrapers {
		if ctx.Err() != nil {
			failed = true
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			break
		}
		sample, err := scraper.Search(ctx, debrid.SearchRequest{
			Query: "test", MaxResults: 3 - len(results),
			Parsed: debrid.ParsedQuery{Title: "test"},
		})
		if err != nil {
			failed = true
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		results = append(results, sample...)
		if len(results) >= 3 {
			results = results[:3]
			break
		}
	}
	response["nameMatchingSuggestion"] = sourceTestNameMatchingSuggestion(results, req.SkipNameFiltering)
	if failed && len(results) == 0 {
		// The connection itself passed, so keep success, but show why the
		// sample search failed instead of hiding it.
		response["message"] = message + ". Filename checks could not be completed."
		if firstErr != nil {
			reason := strings.Join(strings.Fields(firstErr.Error()), " ")
			response["message"] = fmt.Sprintf("%s. Filename checks could not be completed: test search failed: %s", message, reason)
			response["warning"] = reason
		}
	}
	return response
}

var numericSourceFilename = regexp.MustCompile(`(?i)^\d+\.(?:mp4|mkv|m4v|avi|webm|ts|m2ts)$`)

type sourceNameMatchingSuggestion struct {
	Setting        string   `json:"setting"`
	Message        string   `json:"message"`
	NumericCount   int      `json:"numericCount"`
	TestedCount    int      `json:"testedCount"`
	Examples       []string `json:"examples"`
	AlreadyEnabled bool     `json:"alreadyEnabled"`
}

// Inspect the names used by filtering, rather than provider display labels.
// A mixed sample still merits a suggestion: its numeric entries can be excluded.
func sourceTestNameMatchingSuggestion(results []debrid.ScrapeResult, enabled bool) *sourceNameMatchingSuggestion {
	suggestion := &sourceNameMatchingSuggestion{Setting: "skipNameFiltering", TestedCount: len(results), AlreadyEnabled: enabled}
	seen := make(map[string]bool)
	for _, result := range results {
		filename := strings.TrimSpace(result.Title)
		if !numericSourceFilename.MatchString(filename) {
			continue
		}
		suggestion.NumericCount++
		if len(suggestion.Examples) < 3 && !seen[filename] {
			suggestion.Examples = append(suggestion.Examples, filename)
			seen[filename] = true
		}
	}
	if suggestion.NumericCount == 0 {
		return nil
	}
	suggestion.Message = fmt.Sprintf("%d of %d test streams use numeric filenames (%s). Name matching may exclude these streams even when they are correct. Enable Skip Name Matching if you trust this source. Size, quality, and required/excluded term filters still apply.", suggestion.NumericCount, suggestion.TestedCount, strings.Join(suggestion.Examples, ", "))
	if enabled {
		suggestion.Message = fmt.Sprintf("%d of %d test streams use numeric filenames (%s). Skip Name Matching is already enabled for this source.", suggestion.NumericCount, suggestion.TestedCount, strings.Join(suggestion.Examples, ", "))
	}
	return suggestion
}

type sourceTestStream struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	URL           string `json:"url"`
	InfoHash      string `json:"infoHash"`
	BehaviorHints struct {
		Filename string `json:"filename"`
	} `json:"behaviorHints"`
	StreamData struct {
		Type string `json:"type"`
	} `json:"streamData"`
}

// Mirror filename precedence in the addon adapters. Display titles do not
// override numeric filenames in AIOStreams, Comet, or MediaFusion.
func sourceTestStreamResults(streams []sourceTestStream, sourceType string) []debrid.ScrapeResult {
	var results []debrid.ScrapeResult
	for _, stream := range streams {
		if stream.URL == "" && stream.InfoHash == "" {
			continue // Ignore support/install links and other externalUrl-only entries.
		}
		if stream.URL != "" && debrid.IsKnownPlaceholderURL(stream.URL) {
			continue
		}
		filename := strings.TrimSpace(strings.Split(stream.Title, "\n")[0])
		if sourceType == "aiostreams" || sourceType == "mediafusion" || sourceType == "comet" {
			filename = strings.TrimSpace(stream.BehaviorHints.Filename)
			if filename == "" && sourceType != "comet" {
				if parsed, err := url.Parse(stream.URL); err == nil {
					filename = parsed.Query().Get("name")
					if filename == "" {
						path := strings.TrimRight(parsed.Path, "/")
						filename = path[strings.LastIndex(path, "/")+1:]
					}
				}
			}
			if filename == "" {
				filename = strings.TrimSpace(strings.Split(stream.Title, "\n")[0])
			}
		}
		results = append(results, debrid.ScrapeResult{Title: filename})
	}
	return results
}

// addonStreamTestResponse builds the admin test result for a Stremio addon from
// the streams it returned for the test title (The Matrix). Only playable
// entries count: addons answer misconfiguration with HTTP 200 and an in-band
// error "stream" (AIOStreams "[❌]", Comet "[⚠️]"), and a working addon always
// has streams for The Matrix, so an error entry or an empty list fails the
// test with the addon's own message instead of reporting "working". hint is
// appended to error messages to point at the likely fix.
func addonStreamTestResponse(label, sourceType, hint string, streams []sourceTestStream, skipNameFiltering bool) map[string]interface{} {
	var playable []sourceTestStream
	var addonErrors []string
	placeholders := 0
	for _, s := range streams {
		if strings.EqualFold(strings.TrimSpace(s.StreamData.Type), "statistic") {
			continue
		}
		if msg, isErr := debrid.AddonStreamErrorMessage(s.Name, s.Description, s.URL, s.InfoHash, s.StreamData.Type); isErr {
			addonErrors = append(addonErrors, msg)
			continue
		}
		if debrid.IsKnownPlaceholderURL(s.URL) {
			placeholders++
			continue
		}
		if strings.TrimSpace(s.URL) == "" && strings.TrimSpace(s.InfoHash) == "" {
			continue
		}
		playable = append(playable, s)
	}

	withHint := func(msg string) string {
		if hint == "" {
			return msg
		}
		return msg + " " + hint
	}
	switch {
	case len(playable) > 0:
		return map[string]interface{}{
			"success":                true,
			"message":                fmt.Sprintf("%s is working (%d streams found)", label, len(playable)),
			"nameMatchingSuggestion": sourceTestNameMatchingSuggestion(sourceTestStreamResults(playable, sourceType), skipNameFiltering),
		}
	case len(addonErrors) > 0:
		return map[string]interface{}{
			"success": false,
			"error":   withHint(fmt.Sprintf("%s returned an error instead of streams: %s.", label, addonErrors[0])),
		}
	case placeholders > 0:
		return map[string]interface{}{
			"success": false,
			"error":   withHint(fmt.Sprintf("%s only returned placeholder videos (download failed/unavailable) for the test title. Check the debrid service configured in the addon.", label)),
		}
	default:
		return map[string]interface{}{
			"success": false,
			"error":   withHint(fmt.Sprintf("%s returned no playable streams for the test title (The Matrix). Check the addon's configuration and debrid service.", label)),
		}
	}
}

// addonStreamRequestFailure reports a failed test-stream request. The manifest
// loading only proves the server is up; without a stream response the source
// cannot be called working.
func addonStreamRequestFailure(label, hint, detail string) map[string]interface{} {
	msg := fmt.Sprintf("%s manifest loaded, but the test stream request failed: %s.", label, detail)
	if hint != "" {
		msg += " " + hint
	}
	return map[string]interface{}{"success": false, "error": msg}
}
