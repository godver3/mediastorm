package peartube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"novastream/models"
)

var (
	ErrCandidateExpired = errors.New("peartube candidate expired")
	ErrSourceNotCurrent = errors.New("peartube candidate source is no longer current")
	ErrUnavailable      = errors.New("peartube companion unavailable")
	ErrUnsupported      = errors.New("peartube companion capability unsupported")
)

const maxCompanionOpenBody = 64 << 10

// Resolver opens the selected opaque candidate against the currently configured
// companion. It reads Default for every call so an admin configuration change
// takes effect without rebuilding the playback service.
type Resolver struct{}

// Open exchanges an opaque candidate reference for a short-lived companion
// stream route and maps it to MediaStorm's ordinary playback response.
func (*Resolver) Open(ctx context.Context, candidateRef string) (*models.PlaybackResolution, error) {
	candidateRef = strings.TrimSpace(candidateRef)
	if !validCandidateRef(candidateRef) {
		return nil, errors.New("invalid peartube candidate reference")
	}
	client := Default()
	if client == nil {
		return nil, ErrUnavailable
	}

	body, err := json.Marshal(struct {
		CandidateRef string `json:"candidateRef"`
	}{CandidateRef: candidateRef})
	if err != nil {
		return nil, fmt.Errorf("encode companion stream-open request: %w", err)
	}
	target := companionAPIPrefix + "/streams/open"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create companion stream-open request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if err := client.authenticateCompanionRequest(request, body); err != nil {
		return nil, err
	}

	response, err := client.companionHTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return nil, fmt.Errorf("companion stream-open refused redirect status %d", response.StatusCode)
	}
	if response.StatusCode >= http.StatusBadRequest {
		return nil, mapCompanionOpenError(decodeResponse(response, nil))
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("companion stream-open returned unexpected status %d", response.StatusCode)
	}

	opened, err := decodeCompanionOpenResponse(response)
	if err != nil {
		return nil, err
	}
	streamURL, err := ownedCompanionStreamURL(client.baseURL, opened)
	if err != nil {
		return nil, err
	}
	return &models.PlaybackResolution{
		WebDAVPath:   streamURL,
		HealthStatus: "cached",
	}, nil
}

type companionOpenResponse struct {
	URL           string `json:"url"`
	ExpiresAt     int64  `json:"expiresAt"`
	PublicationID string `json:"publicationId"`
	RenditionID   string `json:"renditionId"`
}

func decodeCompanionOpenResponse(response *http.Response) (companionOpenResponse, error) {
	var opened companionOpenResponse
	if response.ContentLength > maxCompanionOpenBody {
		return opened, fmt.Errorf("companion stream-open response exceeds %d bytes", maxCompanionOpenBody)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCompanionOpenBody+1))
	if err != nil {
		return opened, err
	}
	if len(body) > maxCompanionOpenBody {
		return opened, fmt.Errorf("companion stream-open response exceeds %d bytes", maxCompanionOpenBody)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&opened); err != nil {
		return opened, fmt.Errorf("decode companion stream-open response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return opened, errors.New("decode companion stream-open response: trailing JSON value")
		}
		return opened, fmt.Errorf("decode companion stream-open response: %w", err)
	}
	if opened.ExpiresAt <= 0 {
		return opened, errors.New("companion stream-open response has invalid expiresAt")
	}
	if !validCompanionID(opened.PublicationID) || !validCompanionID(opened.RenditionID) {
		return opened, errors.New("companion stream-open response has invalid stream identity")
	}
	return opened, nil
}

func ownedCompanionStreamURL(rawBaseURL string, opened companionOpenResponse) (string, error) {
	if opened.URL == "" || len(opened.URL) > 4096 {
		return "", errors.New("companion stream-open response has invalid URL")
	}
	base, err := url.Parse(rawBaseURL)
	if err != nil {
		return "", errors.New("configured companion URL is invalid")
	}
	reference, err := url.Parse(opened.URL)
	if err != nil || reference.Opaque != "" || reference.User != nil || reference.Fragment != "" {
		return "", errors.New("companion stream-open response has invalid URL")
	}
	if !reference.IsAbs() && (!strings.HasPrefix(opened.URL, "/") || strings.HasPrefix(opened.URL, "//")) {
		return "", errors.New("companion stream-open response URL is not route-scoped")
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != base.Scheme || !strings.EqualFold(resolved.Host, base.Host) || resolved.User != nil {
		return "", errors.New("companion stream-open response URL is not owned by the configured companion")
	}

	streamRoutePrefix := strings.TrimSuffix(base.EscapedPath(), "/") + companionAPIPrefix + "/stream/"
	escapedPath := resolved.EscapedPath()
	if !strings.HasPrefix(escapedPath, streamRoutePrefix) {
		return "", errors.New("companion stream-open response URL is outside the stream route")
	}
	segments := strings.Split(strings.TrimPrefix(escapedPath, streamRoutePrefix), "/")
	if len(segments) != 2 {
		return "", errors.New("companion stream-open response URL is outside the stream route")
	}
	publicationID, publicationErr := url.PathUnescape(segments[0])
	renditionID, renditionErr := url.PathUnescape(segments[1])
	if publicationErr != nil || renditionErr != nil ||
		publicationID != opened.PublicationID || renditionID != opened.RenditionID {
		return "", errors.New("companion stream-open response URL is outside the stream route")
	}
	query := resolved.Query()
	capabilities, ok := query["cap"]
	if !ok || len(query) != 1 || len(capabilities) != 1 || !validCandidateRef(capabilities[0]) ||
		resolved.RawQuery != "cap="+capabilities[0] {
		return "", errors.New("companion stream-open response has invalid stream capability")
	}
	return resolved.String(), nil
}

func mapCompanionOpenError(err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	code := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(apiErr.Code)), "-", "_")
	var stable error
	switch code {
	case "CANDIDATE_EXPIRED":
		stable = ErrCandidateExpired
	case "SOURCE_NOT_CURRENT":
		stable = ErrSourceNotCurrent
	case "BACKEND_UNAVAILABLE", "UNAVAILABLE":
		stable = ErrUnavailable
	case "CAPABILITY_UNAVAILABLE", "UNSUPPORTED":
		stable = ErrUnsupported
	default:
		return err
	}
	return fmt.Errorf("%w: %w", stable, err)
}
