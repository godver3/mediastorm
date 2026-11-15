package nzbfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"novastream/models"
)

var nzbEpisodeCode = regexp.MustCompile(`(?i)\b(s[0-9]+e[0-9]+)\b`)
var nzbReleaseSuffix = regexp.MustCompile(`(?i)[ ._-](?:19[0-9]{2}|20[0-9]{2}|2160p|1080p|720p|bluray|web[ ._-]?dl)\b.*`)

func ContentKey(candidate models.NZBResult) string {
	if value := strings.TrimSpace(candidate.Attributes["nzbContentKey"]); value != "" {
		return value
	}
	// Direct API callers may lack catalog metadata. Other encodings must not
	// create a new title budget just because their release suffix differs.
	title := strings.ToLower(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(candidate.Title))
	if episode := nzbEpisodeCode.FindString(title); episode != "" {
		return "video:" + strings.TrimSpace(strings.Split(title, episode)[0]) + ":" + episode
	}
	return "video:" + strings.TrimSpace(nzbReleaseSuffix.ReplaceAllString(title, ""))
}

func Fetch(ctx context.Context, endpoint, downloadURL string, candidate models.NZBResult) ([]byte, string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, "", fmt.Errorf("configure a valid NZB acquisition gateway")
	}
	token, err := os.ReadFile(os.Getenv("NZB_GATEWAY_TOKEN_FILE"))
	if err != nil || len(bytes.TrimSpace(token)) < 32 {
		return nil, "", fmt.Errorf("the NZB acquisition gateway credential is unavailable")
	}
	release := candidate.GUID
	if release == "" {
		release = downloadURL
	}
	payload, err := json.Marshal(map[string]string{
		"url": downloadURL, "content_key": ContentKey(candidate), "indexer": candidate.Indexer,
		"release_key": release, "name": candidate.Title,
	})
	if err != nil {
		return nil, "", fmt.Errorf("build NZB acquisition request")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/fetch", bytes.NewReader(payload))
		if err != nil {
			return nil, "", fmt.Errorf("build NZB acquisition request")
		}
		req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(token)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("NZB acquisition gateway unavailable; direct downloads are disabled")
		}
		if resp.StatusCode == http.StatusOK {
			const maximum = 50 * 1024 * 1024
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, maximum+1))
			resp.Body.Close()
			if readErr != nil || len(data) > maximum {
				return nil, "", fmt.Errorf("invalid NZB acquisition response")
			}
			name, _ := url.PathUnescape(resp.Header.Get("X-NZB-Filename"))
			if name == "" {
				name = "release.nzb"
			}
			return data, name, nil
		}
		var detail struct {
			Reason string `json:"error"`
			Retry  int    `json:"retry_after"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&detail)
		resp.Body.Close()
		if detail.Retry < 1 {
			detail.Retry = 1
		}
		if detail.Reason == "spacing" && attempt < 2 {
			timer := time.NewTimer(time.Duration(detail.Retry) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, "", ctx.Err()
			case <-timer.C:
				continue
			}
		}
		return nil, "", fmt.Errorf("NZB acquisition paused by download policy (%s); retry after %d seconds", detail.Reason, detail.Retry)
	}
	return nil, "", fmt.Errorf("NZB acquisition is waiting for its download budget")
}
