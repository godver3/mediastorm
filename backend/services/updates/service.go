package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultGitHubRepo = "godver3/mediastorm"

var releaseTagPattern = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)(?:-(\d{8}))?$`)

type Service struct {
	client *http.Client
	repo   string
	ttl    time.Duration

	mu      sync.Mutex
	cached  *githubRelease
	checked time.Time
}

type StatusRequest struct {
	BackendVersion   string
	BackendBuildID   string
	FrontendVersion  string
	FrontendBuildID  string
	FrontendPlatform string
	FrontendDevice   string
	ForceRefresh     bool
}

type StatusResponse struct {
	Backend   ComponentStatus  `json:"backend"`
	Frontend  *ComponentStatus `json:"frontend,omitempty"`
	CheckedAt time.Time        `json:"checkedAt"`
}

type ComponentStatus struct {
	Component       string    `json:"component"`
	CurrentVersion  string    `json:"currentVersion"`
	CurrentBuildID  string    `json:"currentBuildId,omitempty"`
	LatestVersion   string    `json:"latestVersion,omitempty"`
	LatestBuildID   string    `json:"latestBuildId,omitempty"`
	LatestTag       string    `json:"latestTag,omitempty"`
	ReleaseURL      string    `json:"releaseUrl,omitempty"`
	APKDownloadURL  string    `json:"apkDownloadUrl,omitempty"`
	UpdateAvailable bool      `json:"updateAvailable"`
	CheckedAt       time.Time `json:"checkedAt"`
	Error           string    `json:"error,omitempty"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func NewService() *Service {
	repo := strings.TrimSpace(os.Getenv("STRMR_UPDATE_GITHUB_REPO"))
	if repo == "" {
		repo = defaultGitHubRepo
	}
	return &Service{
		client: &http.Client{Timeout: 15 * time.Second},
		repo:   repo,
		ttl:    30 * time.Minute,
	}
}

func (s *Service) Status(ctx context.Context, req StatusRequest) StatusResponse {
	checkedAt := time.Now().UTC()
	release, err := s.latestRelease(ctx, req.ForceRefresh)

	resp := StatusResponse{
		Backend: ComponentStatus{
			Component:      "backend",
			CurrentVersion: cleanVersion(req.BackendVersion),
			CurrentBuildID: strings.TrimSpace(req.BackendBuildID),
			CheckedAt:      checkedAt,
		},
		CheckedAt: checkedAt,
	}
	if req.FrontendVersion != "" {
		resp.Frontend = &ComponentStatus{
			Component:      "frontend",
			CurrentVersion: cleanVersion(req.FrontendVersion),
			CurrentBuildID: strings.TrimSpace(req.FrontendBuildID),
			CheckedAt:      checkedAt,
		}
	}

	if err != nil {
		resp.Backend.Error = err.Error()
		if resp.Frontend != nil {
			resp.Frontend.Error = err.Error()
		}
		return resp
	}

	latestVersion, latestBuildID := ParseReleaseTag(release.TagName)
	resp.Backend = fillLatest(resp.Backend, release, latestVersion, latestBuildID, "")
	if resp.Frontend != nil {
		resp.Frontend = ptr(fillLatest(*resp.Frontend, release, latestVersion, latestBuildID, apkAssetPrefix(req)))
	}
	return resp
}

func (s *Service) latestRelease(ctx context.Context, force bool) (*githubRelease, error) {
	s.mu.Lock()
	if !force && s.cached != nil && time.Since(s.checked) < s.ttl {
		cached := *s.cached
		s.mu.Unlock()
		return &cached, nil
	}
	s.mu.Unlock()

	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", s.repo)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("User-Agent", "strmr-update-checker")

	res, err := s.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("release check failed: %s", res.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(res.Body).Decode(&release); err != nil {
		return nil, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return nil, fmt.Errorf("latest release did not include a tag")
	}

	s.mu.Lock()
	s.cached = &release
	s.checked = time.Now()
	s.mu.Unlock()
	return &release, nil
}

func fillLatest(status ComponentStatus, release *githubRelease, latestVersion, latestBuildID, apkPrefix string) ComponentStatus {
	status.LatestVersion = latestVersion
	status.LatestBuildID = latestBuildID
	status.LatestTag = release.TagName
	status.ReleaseURL = release.HTMLURL
	status.UpdateAvailable = IsNewer(status.CurrentVersion, status.CurrentBuildID, latestVersion, latestBuildID)
	if apkPrefix != "" {
		for _, asset := range release.Assets {
			if strings.HasPrefix(asset.Name, apkPrefix) && strings.HasSuffix(asset.Name, ".apk") {
				status.APKDownloadURL = asset.BrowserDownloadURL
				break
			}
		}
	}
	return status
}

func ParseReleaseTag(tag string) (string, string) {
	tag = strings.TrimSpace(tag)
	matches := releaseTagPattern.FindStringSubmatch(tag)
	if matches == nil {
		return strings.TrimPrefix(tag, "v"), ""
	}
	return matches[1], matches[2]
}

func IsNewer(currentVersion, currentBuildID, latestVersion, latestBuildID string) bool {
	currentVersion = cleanVersion(currentVersion)
	latestVersion = cleanVersion(latestVersion)
	if currentVersion == "" || latestVersion == "" || currentVersion == "unknown" {
		return false
	}
	if cmp := compareVersions(latestVersion, currentVersion); cmp != 0 {
		return cmp > 0
	}
	if latestBuildID == "" || currentBuildID == "" {
		return false
	}
	return compareBuildIDs(latestBuildID, currentBuildID) > 0
}

func compareVersions(a, b string) int {
	ap := parseVersion(a)
	bp := parseVersion(b)
	for i := range ap {
		if ap[i] > bp[i] {
			return 1
		}
		if ap[i] < bp[i] {
			return -1
		}
	}
	return 0
}

func parseVersion(v string) [3]int {
	var out [3]int
	parts := strings.Split(cleanVersion(v), ".")
	for i := 0; i < len(parts) && i < len(out); i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}

func compareBuildIDs(a, b string) int {
	if ai, errA := strconv.ParseInt(a, 10, 64); errA == nil {
		if bi, errB := strconv.ParseInt(b, 10, 64); errB == nil {
			if ai > bi {
				return 1
			}
			if ai < bi {
				return -1
			}
			return 0
		}
	}
	return strings.Compare(a, b)
}

func cleanVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

func apkAssetPrefix(req StatusRequest) string {
	if strings.ToLower(req.FrontendPlatform) != "android" {
		return ""
	}
	if strings.EqualFold(req.FrontendDevice, "tv") {
		return "mediastorm-tv-"
	}
	return "mediastorm-mobile-"
}

func ptr[T any](v T) *T {
	return &v
}
