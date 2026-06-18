package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/debrid"
)

type rawResponse struct {
	Streams []rawStream `json:"streams"`
}

type rawStream struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	ExternalURL string `json:"externalUrl"`
	Behavior    struct {
		Filename string `json:"filename"`
	} `json:"behaviorHints"`
}

type diagSummary struct {
	total             int
	missingURL        int
	externalOnly      int
	directPlaceholder int
	headChecked       int
	headPlaceholder   int
	headSmallContent  int
	headErrorStatus   int
	healthChecked     int
	healthCached      int
	healthNotCached   int
	healthErrors      int
}

func main() {
	var (
		configPath      = flag.String("config", "cache/settings.json", "Path to settings.json")
		scraperName     = flag.String("scraper", "", "AIO scraper name to check (default: all)")
		includeDisabled = flag.Bool("include-disabled", false, "Include disabled AIO scrapers")
		mediaType       = flag.String("type", "series", "Stremio media type: movie or series")
		imdbID          = flag.String("imdb", "", "IMDB ID, e.g. tt0944947")
		season          = flag.Int("season", 1, "Season number for series")
		episode         = flag.Int("episode", 1, "Episode number for series")
		sampleSize      = flag.Int("sample", 20, "Number of top streams to sample")
		timeoutSec      = flag.Int("timeout", 25, "Per-request timeout in seconds")
		ffprobePath     = flag.String("ffprobe", "ffprobe", "ffprobe binary path")
		fastScan        = flag.Bool("fast", false, "Skip full health checks and print HEAD/ffprobe details for each sampled stream")
	)
	flag.Parse()

	if strings.TrimSpace(*imdbID) == "" {
		panic("missing required --imdb")
	}
	if *mediaType != "movie" && *mediaType != "series" {
		panic("--type must be movie or series")
	}
	if *sampleSize <= 0 {
		*sampleSize = 20
	}
	if *timeoutSec <= 0 {
		*timeoutSec = 25
	}

	manager := config.NewManager(*configPath)
	settings, err := manager.Load()
	if err != nil {
		panic(err)
	}

	streamID := strings.TrimSpace(*imdbID)
	if *mediaType == "series" {
		streamID = fmt.Sprintf("%s:%d:%d", streamID, *season, *episode)
	}

	httpClient := &http.Client{Timeout: time.Duration(*timeoutSec) * time.Second}
	health := debrid.NewHealthService(manager)
	health.SetFFProbePath(*ffprobePath)

	fmt.Printf("AIO diagnostics stream=%s/%s sample=%d timeout=%ds\n", *mediaType, streamID, *sampleSize, *timeoutSec)
	for _, sc := range settings.TorrentScrapers {
		if !strings.EqualFold(sc.Type, "aiostreams") {
			continue
		}
		if strings.TrimSpace(*scraperName) != "" && !strings.EqualFold(strings.TrimSpace(sc.Name), strings.TrimSpace(*scraperName)) {
			continue
		}
		if !*includeDisabled && !sc.Enabled {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(sc.URL), "/"), "/manifest.json")
		if base == "" {
			fmt.Printf("[%s] skipped: missing URL\n", sc.Name)
			continue
		}
		if err := runForScraper(httpClient, health, sc.Name, base, *mediaType, streamID, *sampleSize, *ffprobePath, *fastScan); err != nil {
			fmt.Printf("[%s] error: %v\n", sc.Name, err)
		}
	}
}

func runForScraper(httpClient *http.Client, health *debrid.HealthService, name, baseURL, mediaType, streamID string, sampleSize int, ffprobePath string, fastScan bool) error {
	endpoint := fmt.Sprintf("%s/stream/%s/%s.json", baseURL, mediaType, url.PathEscape(streamID))
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d from stream endpoint", resp.StatusCode)
	}

	var payload rawResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}

	sum := diagSummary{total: len(payload.Streams)}
	limit := sampleSize
	if limit > len(payload.Streams) {
		limit = len(payload.Streams)
	}

	for i := range payload.Streams {
		u := streamURL(payload.Streams[i])
		if u == "" {
			sum.missingURL++
			if strings.TrimSpace(payload.Streams[i].ExternalURL) != "" {
				sum.externalOnly++
			}
			continue
		}
		if debrid.IsKnownPlaceholderURL(u) {
			sum.directPlaceholder++
		}
	}

	for i := 0; i < limit; i++ {
		u := streamURL(payload.Streams[i])
		if u == "" {
			continue
		}

		sum.headChecked++
		finalURL, status, contentLength, err := probeHead(httpClient, u)
		if err != nil {
			sum.headErrorStatus++
		} else {
			if status >= 400 {
				sum.headErrorStatus++
			}
			if debrid.IsKnownPlaceholderURL(finalURL) {
				sum.headPlaceholder++
			}
			if contentLength > 0 && contentLength < 1*1024*1024 {
				sum.headSmallContent++
			}
		}

		if fastScan {
			audioCount, probeErr := probeFFprobeAudio(ffprobePath, u)
			host := ""
			if parsed, parseErr := url.Parse(u); parseErr == nil {
				host = parsed.Host
			}
			fmt.Printf("[%s] #%02d head=%d len=%d audio=%d host=%s title=%q\n",
				name, i+1, status, contentLength, audioCount, host, streamTitle(payload.Streams[i]))
			if status == http.StatusMethodNotAllowed || probeErr != nil || audioCount == 0 {
				fmt.Printf("[%s] #%02d concern head=%d probe_err=%v url=%s\n", name, i+1, status, probeErr, u)
			}
			continue
		}

		candidate := models.NZBResult{
			Title:       streamTitle(payload.Streams[i]),
			Link:        u,
			ServiceType: models.ServiceTypeDebrid,
			Attributes: map[string]string{
				"preresolved": "true",
				"stream_url":  u,
			},
		}
		sum.healthChecked++
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		healthRes, healthErr := health.CheckHealth(ctx, candidate, false)
		cancel()
		if healthErr != nil {
			sum.healthErrors++
			continue
		}
		if healthRes != nil && healthRes.Cached {
			sum.healthCached++
		} else {
			sum.healthNotCached++
		}
	}

	fmt.Printf("[%s] total=%d missing_url=%d external_only=%d direct_placeholder=%d\n",
		name, sum.total, sum.missingURL, sum.externalOnly, sum.directPlaceholder)
	fmt.Printf("[%s] sampled=%d head_placeholder=%d head_small=%d head_error=%d\n",
		name, sum.headChecked, sum.headPlaceholder, sum.headSmallContent, sum.headErrorStatus)
	fmt.Printf("[%s] health_cached=%d health_not_cached=%d health_errors=%d\n",
		name, sum.healthCached, sum.healthNotCached, sum.healthErrors)
	return nil
}

func probeFFprobeAudio(ffprobePath, streamURL string) (int, error) {
	probeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "a",
		"-analyzeduration", "5000000",
		"-probesize", "5000000",
		streamURL,
	}

	cmd := exec.CommandContext(probeCtx, ffprobePath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var result struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return 0, err
	}
	return len(result.Streams), nil
}

func streamURL(s rawStream) string {
	u := strings.TrimSpace(s.URL)
	if u != "" {
		return u
	}
	return strings.TrimSpace(s.ExternalURL)
}

func streamTitle(s rawStream) string {
	if trimmed := strings.TrimSpace(s.Behavior.Filename); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(s.Name); trimmed != "" {
		return trimmed
	}
	return "unknown"
}

func probeHead(client *http.Client, rawURL string) (finalURL string, statusCode int, contentLength int64, err error) {
	req, reqErr := http.NewRequest(http.MethodHead, rawURL, nil)
	if reqErr != nil {
		return "", 0, 0, reqErr
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")
	resp, doErr := client.Do(req)
	if doErr != nil {
		return "", 0, 0, doErr
	}
	defer resp.Body.Close()
	return resp.Request.URL.String(), resp.StatusCode, resp.ContentLength, nil
}
