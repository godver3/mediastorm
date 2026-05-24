package metadata

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"novastream/internal/ytdlp"
	"novastream/models"
)

const maxYouTubeSearchLimit = 20

type youtubeSearchRawResult struct {
	ID          string                 `json:"id"`
	URL         string                 `json:"url"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Channel     string                 `json:"channel"`
	Uploader    string                 `json:"uploader"`
	Thumbnails  []youtubeThumbnail     `json:"thumbnails"`
	Duration    flexibleNumber         `json:"duration"`
	ViewCount   flexibleNumber         `json:"view_count"`
	WebpageURL  string                 `json:"webpage_url"`
	Extra       map[string]interface{} `json:"-"`
}

type youtubeThumbnail struct {
	URL    string         `json:"url"`
	Width  flexibleNumber `json:"width"`
	Height flexibleNumber `json:"height"`
}

type flexibleNumber float64

func (n *flexibleNumber) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*n = 0
		return nil
	}
	raw = strings.Trim(raw, `"`)
	if raw == "" {
		*n = 0
		return nil
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		*n = 0
		return nil
	}
	*n = flexibleNumber(parsed)
	return nil
}

func (n flexibleNumber) int() int {
	if n <= 0 {
		return 0
	}
	return int(math.Round(float64(n)))
}

func (n flexibleNumber) int64() int64 {
	if n <= 0 {
		return 0
	}
	return int64(math.Round(float64(n)))
}

func (s *Service) SearchYouTubeVideos(ctx context.Context, query string, limit int) ([]models.YouTubeVideoSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []models.YouTubeVideoSearchResult{}, nil
	}
	if limit < 1 {
		limit = 10
	}
	if limit > maxYouTubeSearchLimit {
		limit = maxYouTubeSearchLimit
	}

	ytdlpPath := "/usr/local/bin/yt-dlp"
	if _, err := exec.LookPath(ytdlpPath); err != nil {
		ytdlpPath = "yt-dlp"
		if _, err := exec.LookPath(ytdlpPath); err != nil {
			return nil, fmt.Errorf("yt-dlp not found")
		}
	}

	searchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	args := []string{
		"--socket-timeout", "10",
		"--retries", "0",
		"--fragment-retries", "0",
		"--dump-json",
		"--flat-playlist",
	}
	if cookiesPath := filepath.Join(s.cacheDir, "yt-dlp-cookies.txt"); cookiesPath != "" {
		if _, err := os.Stat(cookiesPath); err == nil {
			args = append(args, "--cookies", cookiesPath)
		}
	}
	args = ytdlp.AppendProxyArgs(args, s.ytdlpProxyURL())
	args = append(args, fmt.Sprintf("ytsearch%d:%s", limit, query))

	cmd := exec.CommandContext(searchCtx, ytdlpPath, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[metadata] youtube search query=%q limit=%d", query, limit)
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, fmt.Errorf("youtube search failed: %s", errMsg)
	}

	results, err := parseYouTubeSearchResults(&stdout)
	if err != nil {
		return nil, err
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func parseYouTubeSearchResults(r io.Reader) ([]models.YouTubeVideoSearchResult, error) {
	results := []models.YouTubeVideoSearchResult{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw youtubeSearchRawResult
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, fmt.Errorf("parse youtube search result: %w", err)
		}

		title := strings.TrimSpace(raw.Title)
		if title == "" {
			continue
		}

		videoURL := strings.TrimSpace(raw.WebpageURL)
		if videoURL == "" {
			videoURL = strings.TrimSpace(raw.URL)
		}
		if videoURL == "" && strings.TrimSpace(raw.ID) != "" {
			videoURL = "https://www.youtube.com/watch?v=" + strings.TrimSpace(raw.ID)
		}
		if videoURL == "" {
			continue
		}

		results = append(results, models.YouTubeVideoSearchResult{
			ID:           strings.TrimSpace(raw.ID),
			URL:          videoURL,
			Title:        title,
			Description:  strings.TrimSpace(raw.Description),
			Channel:      strings.TrimSpace(raw.Channel),
			Uploader:     strings.TrimSpace(raw.Uploader),
			ThumbnailURL: bestYouTubeThumbnail(raw.Thumbnails),
			Duration:     raw.Duration.int(),
			ViewCount:    raw.ViewCount.int64(),
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read youtube search results: %w", err)
	}
	return results, nil
}

func bestYouTubeThumbnail(thumbnails []youtubeThumbnail) string {
	bestURL := ""
	bestScore := -1
	for _, thumbnail := range thumbnails {
		url := strings.TrimSpace(thumbnail.URL)
		if url == "" {
			continue
		}
		width := thumbnail.Width.int()
		height := thumbnail.Height.int()
		score := width * height
		if score <= 0 {
			score = width + height
		}
		if score > bestScore {
			bestScore = score
			bestURL = url
		}
	}
	return bestURL
}
