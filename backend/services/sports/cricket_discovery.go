package sports

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const cricketDropdownURL = "https://site.api.espn.com/apis/site/v2/leagues/dropdown?region=us&lang=en&sport=cricket"
const cricketDiscoveryTTL = 24 * time.Hour
const cricketDiscoveryMaxBytes = 4 << 20

// CricketSeriesDescriptor is a discovery candidate, not a validated League.
// CompetitionID groups named recurring competitions without treating a trophy
// identifier as a provider series endpoint. Dated series retain separate IDs.
type CricketSeriesDescriptor struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ProviderSeriesID string `json:"providerSeriesId"`
	CompetitionID    string `json:"competitionId,omitempty"`
	Season           string `json:"season,omitempty"`
	Kind             string `json:"kind"`
}

type cricketDiscoverySnapshot struct {
	UpdatedAt time.Time                 `json:"updatedAt"`
	Series    []CricketSeriesDescriptor `json:"series"`
	// The dropdown is capped by the provider; never claim exhaustive discovery.
	Complete bool `json:"complete"`
}

var cricketSeriesIDPattern = regexp.MustCompile(`^[0-9]+$`)
var cricketSeasonPattern = regexp.MustCompile(`\s+(20[0-9]{2}(?:/[0-9]{2,4})?)$`)

func parseCricketDiscovery(data []byte) ([]CricketSeriesDescriptor, error) {
	var payload struct {
		Leagues []struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"leagues"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode cricket dropdown: %w", err)
	}
	rows := payload.Leagues
	result := make([]CricketSeriesDescriptor, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		slug := strings.TrimSpace(row.Slug)
		name := strings.TrimSpace(row.Name)
		// Empty IDs occur in the real feed. Never form root/all or arbitrary URLs.
		if !cricketSeriesIDPattern.MatchString(slug) || name == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		d := CricketSeriesDescriptor{ID: "espn:cricket:" + slug, Name: name, ProviderSeriesID: slug, Kind: "series"}
		if match := cricketSeasonPattern.FindStringSubmatch(name); len(match) > 1 {
			d.Season = match[1]
		}
		if slug == "8048" {
			d.ID = "cricket-8048"
			d.CompetitionID = "cricket-8048"
			d.Kind = "competition"
		}
		result = append(result, d)
	}
	return result, nil
}

// DiscoverCricketSeries persists provider identities beside other sports caches.
// It never mutates the global registry or promotes a dropdown row to live coverage.
// Network failure returns the last good snapshot with the error for health reporting.
func (s *Service) DiscoverCricketSeries(ctx context.Context) ([]CricketSeriesDescriptor, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The shared provider client coalesces network reads. Unique temporary files
	// and atomic rename make concurrent snapshot publication safe without a
	// process-wide lock that could block unrelated services or cancellation.
	path := filepath.Join(s.storageDir, sportsCacheDir, "cricket-series.json")
	var cached cricketDiscoverySnapshot
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cached)
	}
	if len(cached.Series) > 0 && time.Since(cached.UpdatedAt) >= 0 && time.Since(cached.UpdatedAt) < cricketDiscoveryTTL {
		return cached.Series, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cricketDropdownURL, nil)
	if err != nil {
		return cached.Series, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return cached.Series, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cached.Series, fmt.Errorf("cricket discovery HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, cricketDiscoveryMaxBytes+1))
	if err != nil {
		return cached.Series, err
	}
	if len(data) > cricketDiscoveryMaxBytes {
		return cached.Series, fmt.Errorf("cricket dropdown exceeds response limit")
	}
	rows, err := parseCricketDiscovery(data)
	if err != nil {
		return cached.Series, err
	}
	if len(rows) == 0 {
		return cached.Series, fmt.Errorf("cricket dropdown contains no usable series")
	}
	snapshot := cricketDiscoverySnapshot{UpdatedAt: time.Now(), Series: rows, Complete: false}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return rows, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return rows, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "cricket-series-*.tmp")
	if err != nil {
		return rows, err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(encoded); err != nil {
		tmp.Close()
		return rows, err
	}
	if err = tmp.Close(); err != nil {
		return rows, err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return rows, err
	}
	return rows, nil
}
