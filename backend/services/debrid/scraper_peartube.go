package debrid

import (
	"context"
	"log"
	"strconv"
	"strings"

	"novastream/internal/requestsecurity"
	"novastream/services/peartube"
)

// PearTubeScraper searches the authenticated PearTube companion index.
// Results carry only an opaque candidate ref; playback is resolved later.
type PearTubeScraper struct {
	client *peartube.Client
	name   string
}

// NewPearTubeScraper builds a scraper for the relay at relayURL. A relay that
// cannot be addressed yields no scraper rather than a broken one.
func NewPearTubeScraper(relayURL, name string) (*PearTubeScraper, error) {
	client, err := peartube.New(relayURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = "PearTube"
	}
	return &PearTubeScraper{client: client, name: name}, nil
}

func (p *PearTubeScraper) Name() string {
	if p == nil {
		return "PearTube"
	}
	return p.name
}

// Search asks companion v2 for bounded candidates and carries their factual
// metadata through the generic scraper pipeline without a playback locator.
func (p *PearTubeScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	if p == nil || p.client == nil {
		return nil, nil
	}
	title := strings.TrimSpace(req.Parsed.Title)
	if title == "" {
		title = strings.TrimSpace(req.Query)
	}
	if title == "" && strings.TrimSpace(req.TMDBID) == "" && strings.TrimSpace(req.IMDBID) == "" {
		return nil, nil
	}

	search := peartube.SearchRequest{
		Title:      title,
		Year:       req.Parsed.Year,
		Season:     req.Parsed.Season,
		Episode:    req.Parsed.Episode,
		MediaType:  string(req.Parsed.MediaType),
		TMDBID:     req.TMDBID,
		IMDBID:     req.IMDBID,
		MaxResults: req.MaxResults,
	}
	candidates, err := p.client.Search(ctx, search)
	if err != nil {
		return nil, err
	}
	found := peartube.MapCandidates(search, candidates)

	results := make([]ScrapeResult, 0, len(found))
	for _, item := range found {
		attributes := make(map[string]string, len(item.Attributes)+1)
		for key, value := range item.Attributes {
			attributes[key] = value
		}
		attributes["scraper"] = "peartube"
		seeders, _ := strconv.Atoi(attributes["seeders"])

		results = append(results, ScrapeResult{
			Title:       item.Title,
			Indexer:     p.Name(),
			FileIndex:   -1,
			SizeBytes:   item.SizeBytes,
			Seeders:     seeders,
			Provider:    peartube.ProviderName,
			Resolution:  attributes["resolution"],
			Source:      p.Name(),
			Attributes:  attributes,
			ServiceType: item.ServiceType,
		})
	}
	log.Printf("[peartube] %s returned %d deferred candidate(s) for %q from %s", p.Name(), len(results), title, requestsecurity.URLForLog(p.client.BaseURL()))
	return results, nil
}
