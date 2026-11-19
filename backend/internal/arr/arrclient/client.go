// Package arrclient is a minimal Sonarr/Radarr v3 API client used to import
// custom formats and quality profiles from an existing instance and by the
// parity tooling to query an instance's release parser.
package arrclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"novastream/internal/arr"
)

// maxResponseBytes bounds any single API response.
const maxResponseBytes = 32 << 20

// Client talks to one Sonarr or Radarr instance.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New creates a client. baseURL may include a URL base path
// (e.g. http://host:8989/sonarr).
func New(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q: must be http(s)://host[:port][/base]", baseURL)
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("api key is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	// The API key travels in a header Go keeps on redirects, so redirects
	// may not leave the instance's host.
	sameHost := *httpClient
	sameHost.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Host != u.Host {
			return fmt.Errorf("refusing to follow a redirect to %s: the API key would go to another host", req.URL.Host)
		}
		return nil
	}
	return &Client{BaseURL: strings.TrimRight(u.String(), "/"), APIKey: strings.TrimSpace(apiKey), HTTP: &sameHost}, nil
}

// APIError is a non-2xx response.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	return fmt.Sprintf("arr api: HTTP %d: %s", e.Status, body)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	endpoint := c.BaseURL + "/api/v3" + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("arr api: response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("arr api: decode %s: %w", path, err)
	}
	return nil
}

// Status is /system/status.
type Status struct {
	AppName string `json:"appName"`
	Version string `json:"version"`
}

// App returns which app the instance is.
func (s Status) App() (arr.App, error) {
	return arr.ParseApp(s.AppName)
}

// SystemStatus returns the instance's name and version.
func (c *Client) SystemStatus(ctx context.Context) (Status, error) {
	var st Status
	err := c.do(ctx, http.MethodGet, "/system/status", nil, nil, &st)
	return st, err
}

type idName struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type parseQuality struct {
	Quality struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		Source     string `json:"source"`
		Resolution int    `json:"resolution"`
		Modifier   string `json:"modifier"`
	} `json:"quality"`
	Revision arr.Revision `json:"revision"`
}

type parsedInfo struct {
	Quality      parseQuality `json:"quality"`
	Languages    []idName     `json:"languages"`
	ReleaseGroup string       `json:"releaseGroup"`
	ReleaseType  string       `json:"releaseType"`
	FullSeason   bool         `json:"fullSeason"`
	Edition      string       `json:"edition"`
	Year         int          `json:"year"`
}

type parseResponse struct {
	ParsedEpisodeInfo *parsedInfo `json:"parsedEpisodeInfo"`
	ParsedMovieInfo   *parsedInfo `json:"parsedMovieInfo"`
	CustomFormats     []idName    `json:"customFormats"`
}

// Parse runs the instance's release parser on a title.
func (c *Client) Parse(ctx context.Context, title string) (arr.ParityObservation, error) {
	var resp parseResponse
	if err := c.do(ctx, http.MethodGet, "/parse", url.Values{"title": {title}}, nil, &resp); err != nil {
		return arr.ParityObservation{Title: title}, err
	}
	obs := arr.ParityObservation{Title: title}
	info := resp.ParsedEpisodeInfo
	if info == nil {
		info = resp.ParsedMovieInfo
	}
	if info == nil {
		return obs, nil
	}
	obs.Parsed = true
	obs.Quality = info.Quality.Quality.Name
	obs.Version = info.Quality.Revision.Version
	obs.Real = info.Quality.Revision.Real
	obs.IsRepack = info.Quality.Revision.IsRepack
	for _, l := range info.Languages {
		obs.Languages = append(obs.Languages, l.Name)
	}
	obs.ReleaseGroup = info.ReleaseGroup
	obs.ReleaseType = info.ReleaseType
	obs.FullSeason = info.FullSeason
	obs.Edition = info.Edition
	obs.Year = info.Year
	for _, cf := range resp.CustomFormats {
		obs.CustomFormats = append(obs.CustomFormats, cf.Name)
	}
	sort.Strings(obs.CustomFormats)
	return obs, nil
}

// apiField is the *arr API representation of a specification field.
type apiField struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

type apiSpecification struct {
	Name           string     `json:"name"`
	Implementation string     `json:"implementation"`
	Negate         bool       `json:"negate"`
	Required       bool       `json:"required"`
	Fields         []apiField `json:"fields"`
}

type apiCustomFormat struct {
	ID                              int                `json:"id,omitempty"`
	Name                            string             `json:"name"`
	IncludeCustomFormatWhenRenaming bool               `json:"includeCustomFormatWhenRenaming"`
	Specifications                  []apiSpecification `json:"specifications"`
}

// RemoteCustomFormat is a custom format stored on the instance.
type RemoteCustomFormat struct {
	ID     int
	Format arr.CustomFormat
}

func toAPI(cf arr.CustomFormat) apiCustomFormat {
	out := apiCustomFormat{Name: cf.Name, IncludeCustomFormatWhenRenaming: cf.IncludeCustomFormatWhenRenaming}
	for _, s := range cf.Specifications {
		spec := apiSpecification{Name: s.Name, Implementation: s.Implementation, Negate: s.Negate, Required: s.Required}
		keys := make([]string, 0, len(s.Fields))
		for k := range s.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			spec.Fields = append(spec.Fields, apiField{Name: k, Value: s.Fields[k]})
		}
		out.Specifications = append(out.Specifications, spec)
	}
	return out
}

// ListCustomFormats returns every custom format on the instance.
func (c *Client) ListCustomFormats(ctx context.Context) ([]RemoteCustomFormat, error) {
	var raw []json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/customformat", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]RemoteCustomFormat, 0, len(raw))
	for _, item := range raw {
		var head struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(item, &head)
		var cf arr.CustomFormat
		if err := json.Unmarshal(item, &cf); err != nil {
			return nil, fmt.Errorf("decode custom format: %w", err)
		}
		out = append(out, RemoteCustomFormat{ID: head.ID, Format: cf})
	}
	return out, nil
}

// CreateCustomFormat creates a custom format and returns its ID.
func (c *Client) CreateCustomFormat(ctx context.Context, cf arr.CustomFormat) (int, error) {
	var created apiCustomFormat
	err := c.do(ctx, http.MethodPost, "/customformat", nil, toAPI(cf), &created)
	return created.ID, err
}

// UpdateCustomFormat replaces a custom format.
func (c *Client) UpdateCustomFormat(ctx context.Context, remoteID int, cf arr.CustomFormat) error {
	body := toAPI(cf)
	body.ID = remoteID
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/customformat/%d", remoteID), nil, body, nil)
}

// DeleteCustomFormat removes a custom format.
func (c *Client) DeleteCustomFormat(ctx context.Context, remoteID int) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/customformat/%d", remoteID), nil, nil, nil)
}

// RemoteProfile is a quality profile stored on the instance, in API form.
type RemoteProfile struct {
	ID                    int                 `json:"id"`
	Name                  string              `json:"name"`
	UpgradeAllowed        bool                `json:"upgradeAllowed"`
	Cutoff                int                 `json:"cutoff"`
	MinFormatScore        int                 `json:"minFormatScore"`
	CutoffFormatScore     int                 `json:"cutoffFormatScore"`
	MinUpgradeFormatScore int                 `json:"minUpgradeFormatScore"`
	Items                 []RemoteProfileItem `json:"items"`
	FormatItems           []RemoteFormatItem  `json:"formatItems"`
	Language              *idName             `json:"language,omitempty"`
}

// RemoteProfileItem is a quality or quality group in API form. Items are
// ordered lowest to highest preference, the reverse of the UI and TRaSH.
type RemoteProfileItem struct {
	ID      int    `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Quality *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"quality,omitempty"`
	Items   []RemoteProfileItem `json:"items"`
	Allowed bool                `json:"allowed"`
}

// RemoteFormatItem is a custom format score in a profile.
type RemoteFormatItem struct {
	Format int    `json:"format"`
	Name   string `json:"name"`
	Score  int    `json:"score"`
}

// ListQualityProfiles returns every quality profile on the instance.
func (c *Client) ListQualityProfiles(ctx context.Context) ([]RemoteProfile, error) {
	var out []RemoteProfile
	err := c.do(ctx, http.MethodGet, "/qualityprofile", nil, nil, &out)
	return out, err
}

// RemoteQualityDefinition is a quality size definition in API form.
type RemoteQualityDefinition struct {
	Quality struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"quality"`
	Title         string   `json:"title"`
	Weight        int      `json:"weight"`
	MinSize       *float64 `json:"minSize"`
	MaxSize       *float64 `json:"maxSize"`
	PreferredSize *float64 `json:"preferredSize"`
}

// ListQualityDefinitions returns the instance's quality size definitions.
func (c *Client) ListQualityDefinitions(ctx context.Context) ([]RemoteQualityDefinition, error) {
	var out []RemoteQualityDefinition
	err := c.do(ctx, http.MethodGet, "/qualitydefinition", nil, nil, &out)
	return out, err
}
