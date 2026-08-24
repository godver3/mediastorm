// Package peartube integrates a PearTube relay as a peer-to-peer media source.
//
// Search uses the authenticated companion v2 API and returns opaque candidates;
// it never exposes a stream URL. The v1 archive and catalog APIs remain for the
// existing contribution, status, and already-published checks until their
// callers migrate in later plans.
package peartube

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/text/unicode/norm"

	"novastream/config"
)

const (
	// RelayURLEnv names the relay base URL. Its absence, with nothing stored in
	// the admin settings either, is what keeps the whole integration inert for
	// installs that never asked for it.
	RelayURLEnv = "PEARTUBE_RELAY_URL"
	// EnabledEnv force-enables (with the default URL) or force-disables the
	// integration independently of the URL.
	EnabledEnv = "PEARTUBE_ENABLED"
	// CompanionClientEnv and CompanionSharedSecretEnv configure authenticated
	// companion v2 control requests. Credentials are headers only.
	CompanionClientEnv       = "PEARTUBE_COMPANION_CLIENT"
	CompanionSharedSecretEnv = "PEARTUBE_COMPANION_SHARED_SECRET"
	DefaultCompanionClient   = "mediastorm"
	// DefaultRelayURL is where `peartube relay` listens out of the box.
	DefaultRelayURL = "http://127.0.0.1:8174"

	apiPrefix          = "/api/v1"
	companionAPIPrefix = "/api/v2"

	// The v1 catalog is retained only for archive/probe callers.
	catalogPageLimit = 50
	catalogMaxPages  = 20
	catalogTTL       = 30 * time.Second

	requestTimeout = 20 * time.Second

	companionDefaultSearchLimit = 20
	companionMaxCandidates      = 64
	maxCompanionSearchBody      = 1 << 20
	maxCompanionTextBytes       = 512
	maxCompanionSafeInteger     = uint64(1<<53 - 1)
)

var (
	// defaultMu guards the process-wide client. Everything that can seed,
	// search, resolve, or proxy p2p media reads it, and the admin settings page
	// can replace it mid-flight, so it is a lock rather than a sync.Once.
	defaultMu     sync.RWMutex
	defaultClient *Client
	// defaultConfigured records that a configuration has been applied, so the
	// first reader falls back to the environment exactly once instead of
	// re-reading it on every call.
	defaultConfigured bool
)

// Default returns the process-wide relay client, or nil when no relay is
// configured. Every integration point treats nil as "this feature does not
// exist", so an install with no relay behaves exactly as before.
//
// Read it per use rather than capturing it: Configure can replace it when an
// operator saves the admin settings, and a captured client goes stale.
func Default() *Client {
	defaultMu.RLock()
	if defaultConfigured {
		client := defaultClient
		defaultMu.RUnlock()
		return client
	}
	defaultMu.RUnlock()

	defaultMu.Lock()
	defer defaultMu.Unlock()
	if !defaultConfigured {
		// Reached before the admin settings were applied — during startup, or
		// in a test. The environment alone is the answer, which is what this
		// integration did before it was configurable.
		applyLocked(resolve(config.PearTubeSettings{}, os.Getenv))
	}
	return defaultClient
}

// Configure installs an effective configuration process-wide and returns the
// resulting client, which is nil when no relay is configured.
//
// It is called once at startup with the stored settings and again every time
// they are saved, which is what lets an operator point this backend at a relay,
// move it, or switch it off without restarting the container.
func Configure(resolved Resolved) *Client {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	applyLocked(resolved)
	return defaultClient
}

// applyLocked installs a configuration. Must be called with defaultMu held.
//
// An unchanged URL keeps the existing client rather than building an equivalent
// one, so a settings save unrelated to p2p does not throw away the catalog cache
// or re-announce a relay gate that never moved.
func applyLocked(resolved Resolved) {
	defaultConfigured = true

	previous := ""
	if defaultClient != nil {
		previous = defaultClient.baseURL
	}
	if resolved.RelayURL == "" {
		if previous != "" {
			log.Printf("[peartube] relay disabled (was %s)", previous)
		}
		defaultClient = nil
		return
	}
	client, err := New(resolved.RelayURL)
	if err != nil {
		log.Printf("[peartube] relay disabled: %v", err)
		defaultClient = nil
		return
	}
	if client.baseURL == previous {
		return
	}
	log.Printf("[peartube] relay configured at %s", client.baseURL)
	defaultClient = client
}

// New builds a relay client for an explicit base URL. URL credentials, query
// parameters, and fragments are rejected so configuration secrets cannot be
// carried into requests or logs.
func New(rawBaseURL string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil {
		return nil, errors.New("relay URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("relay URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("relay URL is missing a host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("relay URL must not include credentials, query parameters, or a fragment")
	}
	base := strings.TrimSuffix(parsed.Scheme+"://"+parsed.Host+parsed.Path, "/")
	clientID, secret, authErr := companionCredentials(os.Getenv)
	return &Client{
		baseURL: base,
		http:    &http.Client{Timeout: requestTimeout},
		companionHTTP: &http.Client{
			Timeout: requestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		uploads:            &http.Client{},
		companionClient:    clientID,
		companionSecret:    secret,
		companionAuthError: authErr,
	}, nil
}

func companionCredentials(getenv func(string) string) (string, [32]byte, error) {
	var key [32]byte
	clientID := getenv(CompanionClientEnv)
	if clientID == "" {
		clientID = DefaultCompanionClient
	}
	if clientID != strings.TrimSpace(clientID) || len(clientID) > 128 || !validHeaderText(clientID) {
		return "", key, fmt.Errorf("%s must be a non-empty header-safe value of at most 128 bytes", CompanionClientEnv)
	}

	secret := getenv(CompanionSharedSecretEnv)
	if len(secret) != 64 || secret != strings.ToLower(secret) {
		return clientID, key, fmt.Errorf("%s must be 64 lowercase hexadecimal characters", CompanionSharedSecretEnv)
	}
	if _, err := hex.Decode(key[:], []byte(secret)); err != nil {
		return clientID, [32]byte{}, fmt.Errorf("%s must be 64 lowercase hexadecimal characters", CompanionSharedSecretEnv)
	}
	return clientID, key, nil
}

func validHeaderText(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}

// Client talks to one PearTube relay.
type Client struct {
	baseURL       string
	http          *http.Client
	companionHTTP *http.Client
	uploads       *http.Client

	companionClient    string
	companionSecret    [32]byte
	companionAuthError error

	// The v1 catalog state remains for archive/probe callers only.
	mu          sync.Mutex
	cached      []CatalogEntity
	cachedAt    time.Time
	cachedError error
	gateNoted   bool
}

// BaseURL returns the relay origin, normalized without a trailing slash.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// CompanionCandidateV2 is the bounded, URL-free part of one companion v2
// search candidate. Unknown response fields are deliberately ignored.
type CompanionCandidateV2 struct {
	SchemaVersion int                      `json:"schemaVersion"`
	CandidateRef  string                   `json:"candidateRef"`
	Work          *CompanionWorkV2         `json:"work"`
	Publication   *CompanionPublicationV2  `json:"publication"`
	Rendition     *CompanionRenditionV2    `json:"rendition"`
	Asset         *CompanionAssetV2        `json:"asset"`
	Availability  *CompanionAvailabilityV2 `json:"availability"`
}

type CompanionWorkV2 struct {
	Title       string              `json:"title"`
	ReleaseYear *uint64             `json:"releaseYear"`
	Episode     *CompanionEpisodeV2 `json:"episode"`
}

type CompanionEpisodeV2 struct {
	SeriesEntityID string  `json:"seriesEntityId"`
	SeasonNumber   *uint64 `json:"seasonNumber"`
	EpisodeNumber  *uint64 `json:"episodeNumber"`
}

type CompanionPublicationV2 struct {
	PublicationID string `json:"publicationId"`
	PublisherID   string `json:"publisherId"`
}

type CompanionRenditionV2 struct {
	RenditionID     string   `json:"renditionId"`
	Container       string   `json:"container"`
	VideoCodec      string   `json:"videoCodec"`
	Width           *uint64  `json:"width"`
	Height          *uint64  `json:"height"`
	ResolutionLabel string   `json:"resolutionLabel"`
	HDRFormats      []string `json:"hdrFormats"`
	Purpose         string   `json:"purpose"`
	ByteLength      *uint64  `json:"byteLength"`
}

type CompanionAssetV2 struct {
	AssetID    string  `json:"assetId"`
	ByteLength *uint64 `json:"byteLength"`
}

type CompanionAvailabilityV2 struct {
	Peers           *uint64 `json:"peers"`
	CompleteSeeders *uint64 `json:"completeSeeders"`
	ObservedAtMS    *uint64 `json:"observedAtMs"`
	ExpiresAtMS     *uint64 `json:"expiresAtMs"`
}

// Search asks the authenticated companion v2 endpoint for opaque candidates.
// It does not map, verify, open, or otherwise resolve a playback locator.
func (c *Client) Search(ctx context.Context, search SearchRequest) ([]CompanionCandidateV2, error) {
	if c == nil {
		return nil, nil
	}
	target, limit, err := search.companionSearchTarget()
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if err := c.authenticateCompanionRequest(request, nil); err != nil {
		return nil, err
	}

	response, err := c.companionHTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return nil, fmt.Errorf("companion search refused redirect status %d", response.StatusCode)
	}
	if response.StatusCode >= http.StatusBadRequest {
		return nil, decodeResponse(response, nil)
	}
	return decodeCompanionSearchResponse(response, limit)
}

var emptyCompanionBodyHash = blake2b.Sum256(nil)

func (c *Client) authenticateCompanionRequest(request *http.Request, body []byte) error {
	if c.companionAuthError != nil {
		return c.companionAuthError
	}
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("create companion request nonce: %w", err)
	}
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	canonicalTarget := request.URL.EscapedPath()
	if query := encodeCompanionQuery(request.URL.Query()); query != "" {
		canonicalTarget += "?" + query
	}
	request.Header.Set("X-PearTube-Client", c.companionClient)
	request.Header.Set("X-PearTube-Timestamp", timestamp)
	request.Header.Set("X-PearTube-Nonce", nonce)
	bodyHash := blake2b.Sum256(body)
	request.Header.Set("X-PearTube-MAC", companionRequestMACWithBodyHash(
		request.Method,
		canonicalTarget,
		timestamp,
		nonce,
		bodyHash,
		c.companionSecret[:],
	))
	return nil
}

func companionRequestMAC(method, target, timestamp, nonce string, key []byte) string {
	return companionRequestMACWithBodyHash(method, target, timestamp, nonce, emptyCompanionBodyHash, key)
}

func companionRequestMACWithBodyHash(method, target, timestamp, nonce string, bodyHash [32]byte, key []byte) string {
	canonical := strings.Join([]string{
		method,
		target,
		timestamp,
		nonce,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
	mac := hmac.New(sha512.New, key)
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil)[:32])
}

func decodeCompanionSearchResponse(response *http.Response, limit int) ([]CompanionCandidateV2, error) {
	if response.ContentLength > maxCompanionSearchBody {
		return nil, fmt.Errorf("companion search response exceeds %d bytes", maxCompanionSearchBody)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCompanionSearchBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCompanionSearchBody {
		return nil, fmt.Errorf("companion search response exceeds %d bytes", maxCompanionSearchBody)
	}

	var envelope struct {
		Candidates json.RawMessage `json:"candidates"`
		Cursor     json.RawMessage `json:"cursor"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode companion search response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("decode companion search response: trailing JSON value")
		}
		return nil, fmt.Errorf("decode companion search response: %w", err)
	}

	rawCandidates := bytes.TrimSpace(envelope.Candidates)
	if len(rawCandidates) == 0 || rawCandidates[0] != '[' {
		return nil, errors.New("decode companion search response: candidates must be an array")
	}
	var candidates []CompanionCandidateV2
	if err := json.Unmarshal(rawCandidates, &candidates); err != nil {
		return nil, fmt.Errorf("decode companion search candidates: %w", err)
	}
	if len(candidates) > companionMaxCandidates || len(candidates) > limit {
		return nil, fmt.Errorf("companion search returned %d candidates above limit %d", len(candidates), limit)
	}
	for index := range candidates {
		if err := candidates[index].validate(); err != nil {
			return nil, fmt.Errorf("companion search candidate %d: %w", index, err)
		}
	}
	if err := validateCompanionCursor(envelope.Cursor); err != nil {
		return nil, err
	}
	return candidates, nil
}

func validateCompanionCursor(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor == "" || len(cursor) > 512 {
		return errors.New("decode companion search response: invalid cursor")
	}
	for _, character := range cursor {
		if !((character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("._~-", character)) {
			return errors.New("decode companion search response: invalid cursor")
		}
	}
	return nil
}

func (candidate CompanionCandidateV2) validate() error {
	if candidate.SchemaVersion != 0 && candidate.SchemaVersion != 2 {
		return errors.New("schemaVersion is invalid")
	}
	if !validCandidateRef(candidate.CandidateRef) {
		return errors.New("candidateRef is invalid")
	}
	if candidate.Publication == nil || !validCompanionID(candidate.Publication.PublicationID) {
		return errors.New("publicationId is invalid")
	}
	if candidate.Publication.PublisherID != "" && !validCompanionID(candidate.Publication.PublisherID) {
		return errors.New("publisherId is invalid")
	}
	if candidate.Rendition == nil || !validCompanionID(candidate.Rendition.RenditionID) {
		return errors.New("renditionId is invalid")
	}
	if candidate.Asset == nil || !validCompanionID(candidate.Asset.AssetID) {
		return errors.New("assetId is invalid")
	}
	if candidate.Work != nil {
		if err := validateCompanionText(candidate.Work.Title, "work.title", maxCompanionTextBytes, false); err != nil {
			return err
		}
		if err := validateCompanionUint(candidate.Work.ReleaseYear, "work.releaseYear"); err != nil {
			return err
		}
		if candidate.Work.Episode != nil {
			if err := validateCompanionText(candidate.Work.Episode.SeriesEntityID, "work.episode.seriesEntityId", 128, false); err != nil {
				return err
			}
			if err := validateCompanionUint(candidate.Work.Episode.SeasonNumber, "work.episode.seasonNumber"); err != nil {
				return err
			}
			if err := validateCompanionUint(candidate.Work.Episode.EpisodeNumber, "work.episode.episodeNumber"); err != nil {
				return err
			}
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"rendition.container", candidate.Rendition.Container},
		{"rendition.videoCodec", candidate.Rendition.VideoCodec},
		{"rendition.resolutionLabel", candidate.Rendition.ResolutionLabel},
		{"rendition.purpose", candidate.Rendition.Purpose},
	} {
		if err := validateCompanionText(field.value, field.name, 128, false); err != nil {
			return err
		}
	}
	if len(candidate.Rendition.HDRFormats) > 16 {
		return errors.New("rendition.hdrFormats exceeds its bound")
	}
	for _, format := range candidate.Rendition.HDRFormats {
		if err := validateCompanionText(format, "rendition.hdrFormats", 128, true); err != nil {
			return err
		}
	}
	for _, number := range []struct {
		name  string
		value *uint64
	}{
		{"rendition.width", candidate.Rendition.Width},
		{"rendition.height", candidate.Rendition.Height},
		{"rendition.byteLength", candidate.Rendition.ByteLength},
		{"asset.byteLength", candidate.Asset.ByteLength},
	} {
		if err := validateCompanionUint(number.value, number.name); err != nil {
			return err
		}
	}
	if candidate.Availability != nil {
		for _, number := range []struct {
			name  string
			value *uint64
		}{
			{"availability.peers", candidate.Availability.Peers},
			{"availability.completeSeeders", candidate.Availability.CompleteSeeders},
			{"availability.observedAtMs", candidate.Availability.ObservedAtMS},
			{"availability.expiresAtMs", candidate.Availability.ExpiresAtMS},
		} {
			if err := validateCompanionUint(number.value, number.name); err != nil {
				return err
			}
		}
	}
	return nil
}

func validCandidateRef(value string) bool {
	if len(value) != 43 {
		return false
	}
	for _, character := range value {
		if !((character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func validCompanionID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		alphaNumeric := (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9')
		if !alphaNumeric && (index == 0 || !strings.ContainsRune("._:-", character)) {
			return false
		}
	}
	return true
}

func validateCompanionText(value, name string, maximum int, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is empty", name)
		}
		return nil
	}
	if len(value) > maximum || !utf8.ValidString(value) {
		return fmt.Errorf("%s exceeds its bound", name)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

func validateCompanionUint(value *uint64, name string) error {
	if value != nil && *value > maxCompanionSafeInteger {
		return fmt.Errorf("%s exceeds the safe integer bound", name)
	}
	return nil
}

// CatalogSource is one publisher's copy of a rendition.
type CatalogSource struct {
	PublicationID string `json:"publicationId"`
	PublisherID   string `json:"publisherId"`
	RenditionID   string `json:"renditionId"`
	CoreKey       string `json:"coreKey"`
	CoreLength    int64  `json:"coreLength"`
	ByteLength    int64  `json:"byteLength"`
	ContentKind   string `json:"contentKind"`
	MediaProvider string `json:"mediaProvider"`
	MediaID       string `json:"mediaId"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
}

// CatalogEntity is one title (a movie, or a single episode) the swarm can serve.
type CatalogEntity struct {
	EntityID   string          `json:"entityId"`
	EntityKind string          `json:"entityKind"`
	Title      string          `json:"title"`
	Year       int             `json:"year"`
	Sources    []CatalogSource `json:"sources"`
}

type catalogPage struct {
	Entities   []CatalogEntity `json:"entities"`
	NextCursor string          `json:"nextCursor"`
}

// ArchiveJob is the relay's 202 answer to a seed submission.
type ArchiveJob struct {
	JobID      string `json:"jobId"`
	Status     string `json:"status"`
	EntityHint string `json:"entityHint"`
}

// ArchiveSource identifies the publication a finished seed produced.
type ArchiveSource struct {
	EntityID      string `json:"entityId"`
	PublicationID string `json:"publicationId"`
	ManifestID    string `json:"manifestId"`
	PublisherID   string `json:"publisherId"`
	RenditionID   string `json:"renditionId"`
	CoreKey       string `json:"coreKey"`
	CoreLength    int64  `json:"coreLength"`
	ByteLength    int64  `json:"byteLength"`
}

// ArchiveStatus is the relay's answer for a seed job in progress or finished.
type ArchiveStatus struct {
	JobID  string         `json:"jobId"`
	Status string         `json:"status"`
	Title  string         `json:"title"`
	Error  string         `json:"error"`
	Source *ArchiveSource `json:"source"`
}

// APIError carries the relay's structured error body.
type APIError struct {
	Status  int
	Code    string
	Message string
	Field   string
}

func (e *APIError) Error() string {
	parts := make([]string, 0, 3)
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Field != "" {
		parts = append(parts, "field="+e.Field)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("relay returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("relay returned HTTP %d: %s", e.Status, strings.Join(parts, ": "))
}

// The relay gates enumeration and byte serving on how it is bound. Bound to
// loopback it answers freely; bound to 0.0.0.0 or any other interface it
// refuses GET /api/v1/catalog and GET /api/v1/stream until the operator opts
// in. Seeding (POST /api/v1/archive) is never gated.
//
// This backend usually runs in a container and therefore reaches the relay over
// a non-loopback address, so the gate is the expected first-run state rather
// than a malfunction. It has to be named as such, or an unconfigured relay
// looks like a broken integration.
const (
	// openAccessNotEnabledCode is the relay's error code for that refusal. Only
	// the code is stable: the message embeds the actual bind address.
	openAccessNotEnabledCode = "OPEN_ACCESS_NOT_ENABLED"

	// NotOpenRemedy is the operator action that clears the gate, worded so it
	// can be shown to a person verbatim.
	NotOpenRemedy = "restart the relay with --api-open (or PEARTUBE_ARCHIVE_API_OPEN=1)"
)

// ErrRelayNotOpen marks a relay that is up and answering but refusing to
// enumerate or serve media because open access was never enabled.
//
// It is deliberately distinct from "the relay is unreachable" and from "the
// relay has nothing matching": this one is cleared by an operator, not by
// retrying or by searching for something else.
var ErrRelayNotOpen = errors.New("peartube relay will not enumerate or serve media until open access is enabled: " + NotOpenRemedy)

// sourceRefusedPrefix is the shared prefix of every relay error code that means
// "the URL you gave me is not one I will fetch": a bad scheme, embedded
// credentials, a host that is not publicly routable, a name that will not
// resolve, or a missing/ambiguous source. They are all the caller's problem, so
// they must be distinguishable from a relay that is merely broken or down.
const sourceRefusedPrefix = "SOURCE_"

// ErrSourceRefused marks a URL seed the relay declined to fetch because of the
// URL itself.
//
// Nothing about the relay needs fixing when this happens, and retrying the same
// URL fails identically: the caller has to supply a different one. The specific
// reason stays on the APIError's Code.
var ErrSourceRefused = errors.New("peartube relay refused the source URL")

// IsSourceRefused reports whether the relay rejected the seed's source URL.
func IsSourceRefused(err error) bool {
	return errors.Is(err, ErrSourceRefused)
}

// Unwrap lets errors.Is see the relay's actionable refusals — the open-access
// gate, and a source URL the relay will not fetch — through the structured
// error, so callers match on a sentinel instead of re-deriving an error code.
func (e *APIError) Unwrap() error {
	switch {
	case e.Code == openAccessNotEnabledCode:
		return ErrRelayNotOpen
	case strings.HasPrefix(e.Code, sourceRefusedPrefix):
		return ErrSourceRefused
	}
	return nil
}

// IsRelayNotOpen reports whether err is the relay's open-access gate.
func IsRelayNotOpen(err error) bool {
	return errors.Is(err, ErrRelayNotOpen)
}

// noteGate reports a change in the v1 catalog's open-access state, and says
// nothing while it holds steady. Must be called with c.mu held. Repeated
// archive/probe checks therefore announce an operator-actionable gate once.
func (c *Client) noteGate(err error) {
	gated := IsRelayNotOpen(err)
	if gated == c.gateNoted {
		return
	}
	c.gateNoted = gated
	if !gated {
		log.Printf("[peartube] relay %s is serving media again", c.baseURL)
		return
	}
	detail := gateDetail(err)
	// The relay's own message usually ends in the same remedy; only append it
	// when the relay did not already say it.
	if !strings.Contains(detail, NotOpenRemedy) {
		detail += " -- " + NotOpenRemedy
	}
	log.Printf("[peartube] WARN: relay %s is reachable but refuses to enumerate or serve media: %s",
		c.baseURL, detail)
}

// gateDetail is the relay's own explanation, which names the address it is
// bound to. It falls back to the remedy when the relay sent no message.
func gateDetail(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if message := strings.TrimSpace(apiErr.Message); message != "" {
			return message
		}
	}
	return NotOpenRemedy
}

// RelayState is a plain verdict on what the relay can currently do, for the
// p2p status endpoint. "Reachable but not open" is its own answer: the relay is
// up and still accepts seeds, it just will not enumerate or serve media yet.
type RelayState struct {
	RelayURL  string `json:"relayUrl"`
	Reachable bool   `json:"reachable"`
	// NotOpen is the operator-fixable state: the relay answered with its
	// open-access refusal. Remedy says what to do about it.
	NotOpen bool `json:"notOpen"`
	// SeedingAvailable records that POST /api/v1/archive is not gated, so a
	// relay that refuses to enumerate can still be seeded to.
	SeedingAvailable bool   `json:"seedingAvailable"`
	CatalogEntities  int    `json:"catalogEntities"`
	Remedy           string `json:"remedy,omitempty"`
	Detail           string `json:"detail,omitempty"`
}

// Probe asks the relay what its retained v1 archive/catalog surface can do.
func (c *Client) Probe(ctx context.Context) RelayState {
	if c == nil {
		return RelayState{}
	}
	state := RelayState{RelayURL: c.baseURL}
	entities, err := c.Catalog(ctx)
	switch {
	case err == nil:
		state.Reachable = true
		state.CatalogEntities = len(entities)
	case IsRelayNotOpen(err):
		state.Reachable = true
		state.NotOpen = true
		state.Remedy = NotOpenRemedy
		state.Detail = gateDetail(err)
	default:
		// An APIError means the relay answered, just not with a catalog; a
		// transport error means nothing answered at all.
		var apiErr *APIError
		state.Reachable = errors.As(err, &apiErr)
		state.Detail = err.Error()
	}
	// Seeding is only gated by whether the relay is there to accept it.
	state.SeedingAvailable = state.Reachable
	return state
}

// Catalog returns every v1 entity the relay can serve, cached briefly for
// archive/probe and already-published checks.
func (c *Client) Catalog(ctx context.Context) ([]CatalogEntity, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.cachedAt) < catalogTTL {
		return c.cached, c.cachedError
	}
	entities, err := c.fetchCatalog(ctx)
	// A cancelled request says nothing about the relay, so it must not poison
	// the cache for the next caller.
	if errors.Is(err, context.Canceled) {
		return nil, err
	}
	c.cached, c.cachedError, c.cachedAt = entities, err, time.Now()
	c.noteGate(err)
	return entities, err
}

func (c *Client) fetchCatalog(ctx context.Context) ([]CatalogEntity, error) {
	var (
		entities []CatalogEntity
		cursor   string
	)
	for range catalogMaxPages {
		query := url.Values{"limit": {strconv.Itoa(catalogPageLimit)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var decoded catalogPage
		if err := c.getJSON(ctx, apiPrefix+"/catalog?"+query.Encode(), &decoded); err != nil {
			return nil, err
		}
		entities = append(entities, decoded.Entities...)
		if decoded.NextCursor == "" {
			return entities, nil
		}
		cursor = decoded.NextCursor
	}
	log.Printf("[peartube] catalog walk stopped at %d pages (%d entities)", catalogMaxPages, len(entities))
	return entities, nil
}

// ArchiveStatus polls one seed job. Companion source jobs use the authenticated
// v2 control route; retained URL jobs continue on the v1 archive route.
func (c *Client) ArchiveStatus(ctx context.Context, jobID string) (*ArchiveStatus, error) {
	if c == nil {
		return nil, errors.New("peartube relay is not configured")
	}
	jobID = strings.TrimSpace(jobID)
	if !validSourceJobID(jobID) {
		return nil, errors.New("job id is required")
	}
	if strings.HasPrefix(jobID, "ing_") {
		return c.companionArchiveStatus(ctx, jobID)
	}
	var status ArchiveStatus
	if err := c.getJSON(ctx, apiPrefix+"/archive/"+url.PathEscape(jobID), &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) companionArchiveStatus(ctx context.Context, jobID string) (*ArchiveStatus, error) {
	job, err := c.fetchIngestJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	status := &ArchiveStatus{
		JobID:  jobID,
		Status: job.State,
		Error:  job.ErrorCode,
	}
	if job.State == "completed" {
		status.Source = &ArchiveSource{
			PublicationID: job.PublicationID,
			ManifestID:    job.ManifestID,
			RenditionID:   job.RenditionID,
			CoreKey:       job.CoreKey,
		}
	}
	return status, nil
}

// IngestJob is the relay's own account of one granted ingest job. It carries the
// two facts a resubmission turns on and ArchiveStatus does not expose: the
// offset the transfer actually reached, and whether the relay holds those bytes
// to be a truthful prefix of the title the job asked for.
type IngestJob struct {
	JobID         string
	State         string
	BytesReceived int64
	ExpectedBytes int64
	ErrorCode     string
	// Recoverable is the relay's verdict that a resubmission may resume from
	// BytesReceived rather than start the title again.
	Recoverable bool
}

// IngestJob asks the relay how one granted ingest job stands.
//
// Nothing used to ask. A job the relay loses to a restart ends `failed` and
// `recoverable`, keeps every byte it confirmed, and waits for a capability whose
// grant lifetime is far shorter than the archive it was serving — so only this
// process can revive it, and until something asked, those bytes sat on the
// relay's disk while the title's claim blocked any resubmission.
func (c *Client) IngestJob(ctx context.Context, jobID string) (*IngestJob, error) {
	if c == nil {
		return nil, errors.New("peartube relay is not configured")
	}
	jobID = strings.TrimSpace(jobID)
	if !strings.HasPrefix(jobID, "ing_") || !validSourceJobID(jobID) {
		return nil, errors.New("companion ingest job id is required")
	}
	job, err := c.fetchIngestJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return &IngestJob{
		JobID:         job.JobID,
		State:         job.State,
		BytesReceived: job.BytesReceived,
		ExpectedBytes: job.ExpectedBytes,
		ErrorCode:     job.ErrorCode,
		Recoverable:   job.Recoverable,
	}, nil
}

// fetchIngestJob is the one authenticated read of the companion ingest job
// route. Both the status proxy and the revival query decode the same answer, so
// neither can drift from what the relay actually reports.
func (c *Client) fetchIngestJob(ctx context.Context, jobID string) (*companionIngestPublicJob, error) {
	target := companionAPIPrefix + "/ingest/jobs/" + url.PathEscape(jobID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if err := c.authenticateCompanionRequest(request, nil); err != nil {
		return nil, err
	}
	response, err := c.companionHTTP.Do(request)
	if err != nil {
		return nil, errors.New("poll companion ingest job")
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return nil, errors.New("companion ingest status refused redirect")
	}
	if response.StatusCode != http.StatusOK {
		return nil, decodeResponse(response, nil)
	}
	var envelope struct {
		Job companionIngestPublicJob `json:"job"`
	}
	if err := decodeResponse(response, &envelope); err != nil {
		return nil, err
	}
	if envelope.Job.JobID != jobID || envelope.Job.State == "" {
		return nil, errors.New("companion returned a mismatched ingest job")
	}
	return &envelope.Job, nil
}

// CompanionNetworkPolicy is the complete explicit policy snapshot accepted by
// the authenticated companion control boundary.
type CompanionNetworkPolicy struct {
	PolicyVersion           int    `json:"policyVersion"`
	ConsentVersion          int    `json:"consentVersion"`
	MigrationRequired       bool   `json:"migrationRequired"`
	ContributeWatchedMedia  bool   `json:"contributeWatchedMedia"`
	ArchiveEnabled          bool   `json:"archiveEnabled"`
	ContributionBudgetBytes int64  `json:"contributionBudgetBytes"`
	ArchiveBudgetBytes      int64  `json:"archiveBudgetBytes"`
	UploadPermission        string `json:"uploadPermission"`
	UploadCeilingBytes      int64  `json:"uploadCeilingBytes"`
}

// ApplyNetworkPolicy reconciles the relay before any ingest capability or job
// is created. The existing companion MAC authenticates this control request.
func (c *Client) ApplyNetworkPolicy(ctx context.Context, policy CompanionNetworkPolicy) error {
	if c == nil {
		return errors.New("peartube relay is not configured")
	}
	body, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	defer func() {
		for index := range body {
			body[index] = 0
		}
	}()
	target := companionAPIPrefix + "/policy"
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if err := c.authenticateCompanionRequest(request, body); err != nil {
		return err
	}
	response, err := c.companionHTTP.Do(request)
	if err != nil {
		return errors.New("apply companion network policy")
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return errors.New("companion network policy refused redirect")
	}
	if response.StatusCode != http.StatusOK {
		return decodeResponse(response, nil)
	}
	return nil
}

// CancelArchive cancels an active Plan 11 ingest job. Automatic contribution
// never submits legacy URL jobs, so their v1 identifiers are deliberately not
// accepted here.
func (c *Client) CancelArchive(ctx context.Context, jobID string) error {
	if c == nil {
		return errors.New("peartube relay is not configured")
	}
	jobID = strings.TrimSpace(jobID)
	if !strings.HasPrefix(jobID, "ing_") || !validSourceJobID(jobID) {
		return errors.New("companion ingest job id is required")
	}
	target := companionAPIPrefix + "/ingest/jobs/" + url.PathEscape(jobID)
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if err := c.authenticateCompanionRequest(request, nil); err != nil {
		return err
	}
	response, err := c.companionHTTP.Do(request)
	if err != nil {
		return errors.New("cancel companion ingest job")
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return errors.New("companion ingest cancellation refused redirect")
	}
	if response.StatusCode != http.StatusOK {
		return decodeResponse(response, nil)
	}
	return nil
}

// ArchiveCoordinates are the TMDB coordinates a seed is published under. Both
// seed transports carry exactly this set; only where the bytes come from
// differs, so the coordinates live in one place.
type ArchiveCoordinates struct {
	ContentKind string // "movie" or "episode"
	TMDBID      string
	TMDBTitle   string
	TMDBYear    int
	TMDBSeason  int
	TMDBEpisode int
	PosterPath  string
	Overview    string
	Runtime     int
	Genres      string
}

// Validate rejects coordinates the relay would reject, so an obvious mistake
// costs no round trip. It is exported because the caller that assembled the
// coordinates is the one that can report the problem as a client error rather
// than as a relay failure.
func (c ArchiveCoordinates) Validate() error {
	switch c.ContentKind {
	case "movie":
		if c.TMDBSeason != 0 || c.TMDBEpisode != 0 {
			return errors.New("a movie cannot carry season or episode coordinates")
		}
	case "episode":
		if c.TMDBSeason < 1 || c.TMDBEpisode < 1 {
			return errors.New("an episode requires tmdbSeason and tmdbEpisode")
		}
	default:
		return fmt.Errorf("contentKind must be movie or episode, got %q", c.ContentKind)
	}
	if strings.TrimSpace(c.TMDBID) == "" {
		return errors.New("tmdbId is required")
	}
	if strings.TrimSpace(c.TMDBTitle) == "" {
		return errors.New("tmdbTitle is required")
	}
	return nil
}

const (
	RetentionClassContributionCache = "contribution-cache"
	RetentionClassArchivePin        = "archive-pin"
)

// ArchiveRequest describes a local file to publish into the swarm. The relay
// receives the bytes.
type ArchiveRequest struct {
	FilePath               string
	IdempotencyKey         string
	RetentionClass         string
	SourceGrantPolicyEpoch uint64 `json:"-"`
	ArchiveCoordinates
}

func (r ArchiveRequest) Validate() error {
	if strings.TrimSpace(r.FilePath) == "" {
		return errors.New("filePath is required")
	}
	if r.RetentionClass != "" &&
		r.RetentionClass != RetentionClassContributionCache &&
		r.RetentionClass != RetentionClassArchivePin {
		return errors.New("retention class is invalid")
	}
	return r.ArchiveCoordinates.Validate()
}

type companionIngestMediaContext struct {
	Kind             string `json:"kind"`
	Namespace        string `json:"namespace,omitempty"`
	Identifier       string `json:"identifier,omitempty"`
	SeriesNamespace  string `json:"seriesNamespace,omitempty"`
	SeriesIdentifier string `json:"seriesIdentifier,omitempty"`
	SeasonNumber     int    `json:"seasonNumber,omitempty"`
	EpisodeNumber    int    `json:"episodeNumber,omitempty"`
}

type companionIngestMeasuredFacts struct {
	Title      string `json:"title"`
	ByteLength int64  `json:"byteLength"`
	DurationMS int64  `json:"durationMs,omitempty"`
	Container  string `json:"container"`
}

type companionIngestExpected struct {
	ByteLength int64 `json:"byteLength"`
	// The job id is blake2b over the canonical JSON of this request, and the
	// relay computes it over its NORMALIZED form, so the shape here has to match
	// that form byte for byte or the ids diverge and the submission comes back
	// as a mismatched job.
	//
	// The relay's normalizeExpected always emits sha256 - null when there is no
	// up-front digest - and emits etag only when one was supplied. So sha256 is
	// a pointer that serializes as null rather than being omitted, and etag is
	// omitted when empty. A granted remote source cannot state a digest without
	// pulling the whole title through this process, which is the cost this path
	// exists to avoid, so null is the honest value and the relay still tells it
	// apart from a digest that is wrong.
	SHA256 *string `json:"sha256"`
	ETag   string  `json:"etag,omitempty"`
}

type companionIngestBundleProvenance struct {
	SourceKind  string `json:"sourceKind"`
	ReleaseName string `json:"releaseName,omitempty"`
}

type companionIngestRequest struct {
	RetentionClass   string                           `json:"retentionClass"`
	MediaContext     companionIngestMediaContext      `json:"mediaContext"`
	MeasuredFacts    companionIngestMeasuredFacts     `json:"measuredFacts"`
	Expected         companionIngestExpected          `json:"expected"`
	BundleProvenance *companionIngestBundleProvenance `json:"bundleProvenance,omitempty"`
}

type companionIngestSubmission struct {
	IdempotencyKey   string                 `json:"idempotencyKey"`
	Request          companionIngestRequest `json:"request"`
	SourceCapability string                 `json:"sourceCapability"`
}

type companionIngestPublicJob struct {
	JobID         string `json:"jobId"`
	State         string `json:"state"`
	PublicationID string `json:"publicationId"`
	ManifestID    string `json:"manifestId"`
	RenditionID   string `json:"renditionId"`
	AssetID       string `json:"assetId"`
	CoreKey       string `json:"coreKey"`
	ErrorCode     string `json:"errorCode"`
	// BytesReceived, ExpectedBytes and Recoverable are what a resubmission needs
	// from a job that ended badly: how far it got, how far it had to go, and
	// whether the relay will let a fresh capability carry on from there.
	BytesReceived int64 `json:"bytesReceived"`
	ExpectedBytes int64 `json:"expectedBytes"`
	Recoverable   bool  `json:"recoverable"`
}

// sourceContainer names the container from whatever path identifies the source.
func sourceContainer(path string) string {
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if container == "" {
		container = "bin"
	}
	return container
}

// optionalDigest keeps an absent digest distinguishable from an empty one on the
// wire: the relay's canonical form carries sha256 as null when a source could
// not state it, and the job id is hashed over that form.
func optionalDigest(digest string) *string {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return nil
	}
	return &digest
}

// companionSourceRequest describes the ingest one granted source will feed. The
// container arrives from the caller because a local file and a remote stream
// path name it in different places.
func companionSourceRequest(coordinates ArchiveCoordinates, retentionClass, container string, source IssuedSourceGrant) companionIngestRequest {
	context := companionIngestMediaContext{Kind: coordinates.ContentKind}
	if coordinates.ContentKind == "movie" {
		context.Namespace = "tmdb"
		context.Identifier = strings.TrimSpace(coordinates.TMDBID)
	} else {
		context.SeriesNamespace = "tmdb"
		context.SeriesIdentifier = strings.TrimSpace(coordinates.TMDBID)
		context.SeasonNumber = coordinates.TMDBSeason
		context.EpisodeNumber = coordinates.TMDBEpisode
	}
	measured := companionIngestMeasuredFacts{
		Title:      norm.NFC.String(strings.TrimSpace(coordinates.TMDBTitle)),
		ByteLength: source.Length,
		Container:  container,
	}
	if coordinates.Runtime > 0 {
		measured.DurationMS = int64(coordinates.Runtime) * int64(time.Minute/time.Millisecond)
	}
	if retentionClass == "" {
		retentionClass = RetentionClassArchivePin
	}
	return companionIngestRequest{
		RetentionClass: retentionClass,
		MediaContext:   context,
		MeasuredFacts:  measured,
		Expected: companionIngestExpected{
			ByteLength: source.Length,
			SHA256:     optionalDigest(source.SHA256),
			ETag:       source.ETag,
		},
	}
}

func canonicalCompanionJSON(value any) ([]byte, error) {
	serialized, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	defer func() {
		for index := range serialized {
			serialized[index] = 0
		}
	}()
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(serialized))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normalized); err != nil {
		return nil, err
	}
	result := bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
	result = bytes.ReplaceAll(result, []byte(`\u2028`), []byte("\u2028"))
	result = bytes.ReplaceAll(result, []byte(`\u2029`), []byte("\u2029"))
	return result, nil
}

func companionIngestFingerprint(request companionIngestRequest) (string, error) {
	canonical, err := canonicalCompanionJSON(request)
	if err != nil {
		return "", err
	}
	fingerprintInput := append([]byte("peartube.companion.ingest.request.v1\x00"), canonical...)
	fingerprint := blake2b.Sum256(fingerprintInput)
	for index := range fingerprintInput {
		fingerprintInput[index] = 0
	}
	return hex.EncodeToString(fingerprint[:]), nil
}

func companionIngestJobID(idempotencyKey string, request companionIngestRequest) (string, error) {
	fingerprint, err := companionIngestFingerprint(request)
	if err != nil {
		return "", err
	}
	jobInput := []byte("peartube.companion.ingest.job.v1\x00" + idempotencyKey + "\x00" + fingerprint)
	jobDigest := blake2b.Sum256(jobInput)
	for index := range jobInput {
		jobInput[index] = 0
	}
	return "ing_" + hex.EncodeToString(jobDigest[:16]), nil
}

func companionEntityHint(coordinates ArchiveCoordinates) string {
	if coordinates.ContentKind == "episode" {
		return fmt.Sprintf("show:%s:s%d:e%d", strings.TrimSpace(coordinates.TMDBID), coordinates.TMDBSeason, coordinates.TMDBEpisode)
	}
	return "movie:" + strings.TrimSpace(coordinates.TMDBID)
}

// ArchiveSource grants one authenticated companion job resumable access to an
// already-authorized local file. Only the opaque capability crosses the
// control request; the local path never leaves this process.
func (c *Client) ArchiveSource(ctx context.Context, req ArchiveRequest, registry *SourceGrantRegistry) (*ArchiveJob, error) {
	if err := c.grantedIngestGuard(registry); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if !validSourceJobID(idempotencyKey) {
		return nil, errors.New("idempotency key is required")
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(req.FilePath)))
	if separator := strings.IndexByte(contentType, ';'); separator >= 0 {
		contentType = contentType[:separator]
	}
	prepared, err := registry.Prepare(req.FilePath, contentType)
	if err != nil {
		return nil, err
	}
	defer prepared.Close()
	ingestRequest := companionSourceRequest(
		req.ArchiveCoordinates, req.RetentionClass, sourceContainer(req.FilePath), prepared.facts())
	return c.submitGrantedIngest(ctx, registry, prepared, grantedIngestSubmission{
		IdempotencyKey: idempotencyKey,
		PolicyEpoch:    req.SourceGrantPolicyEpoch,
		Request:        ingestRequest,
		Coordinates:    req.ArchiveCoordinates,
	})
}

// ArchiveRemoteRequest describes a range-readable remote title the companion
// pulls through an authenticated grant.
//
// This is what replaces handing over a CDN address. The companion never learns
// where the bytes live, so its transfer no longer dies when a debrid address
// expires or a viewer closes the player: it asks this process for a range, and
// this process re-resolves the address underneath.
type ArchiveRemoteRequest struct {
	Source                 RemoteSource
	IdempotencyKey         string
	RetentionClass         string
	SourceGrantPolicyEpoch uint64
	ArchiveCoordinates
}

func (r ArchiveRemoteRequest) Validate() error {
	if strings.TrimSpace(r.Source.StreamPath) == "" {
		return errors.New("streamPath is required")
	}
	if r.Source.Reader == nil {
		return errors.New("a streaming provider is required")
	}
	if r.RetentionClass != "" &&
		r.RetentionClass != RetentionClassContributionCache &&
		r.RetentionClass != RetentionClassArchivePin {
		return errors.New("retention class is invalid")
	}
	return r.ArchiveCoordinates.Validate()
}

// ArchiveRemoteSource grants one authenticated companion job resumable range
// access to a remote title. The stream path, the provider credential and every
// resolved address stay inside this process.
func (c *Client) ArchiveRemoteSource(ctx context.Context, req ArchiveRemoteRequest, registry *SourceGrantRegistry) (*ArchiveJob, error) {
	if err := c.grantedIngestGuard(registry); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if !validSourceJobID(idempotencyKey) {
		return nil, errors.New("idempotency key is required")
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(req.Source.StreamPath)))
	if separator := strings.IndexByte(contentType, ';'); separator >= 0 {
		contentType = contentType[:separator]
	}
	prepared, err := registry.PrepareRemote(ctx, req.Source, contentType)
	if err != nil {
		return nil, err
	}
	defer prepared.Close()
	ingestRequest := companionSourceRequest(
		req.ArchiveCoordinates, req.RetentionClass, sourceContainer(req.Source.StreamPath), prepared.facts())
	return c.submitGrantedIngest(ctx, registry, prepared, grantedIngestSubmission{
		IdempotencyKey: idempotencyKey,
		PolicyEpoch:    req.SourceGrantPolicyEpoch,
		Request:        ingestRequest,
		Coordinates:    req.ArchiveCoordinates,
	})
}

func (c *Client) grantedIngestGuard(registry *SourceGrantRegistry) error {
	if c == nil {
		return errors.New("peartube relay is not configured")
	}
	if registry == nil {
		return errors.New("peartube source grants are unavailable")
	}
	return c.companionAuthError
}

// grantedIngestSubmission is everything a granted ingest needs that does not
// depend on which kind of source is behind it.
type grantedIngestSubmission struct {
	IdempotencyKey string
	PolicyEpoch    uint64
	Request        companionIngestRequest
	Coordinates    ArchiveCoordinates
}

// submitGrantedIngest derives the job identity, issues the capability for the
// prepared source, and hands the companion the job. It takes ownership of
// prepared: a submission the companion did not accept revokes the grant, so a
// refusal never leaves a live capability behind.
func (c *Client) submitGrantedIngest(ctx context.Context, registry *SourceGrantRegistry, prepared *PreparedSource, ingest grantedIngestSubmission) (*ArchiveJob, error) {
	jobID, err := companionIngestJobID(ingest.IdempotencyKey, ingest.Request)
	if err != nil {
		return nil, errors.New("encode companion ingest identity")
	}
	issued, err := registry.Issue(prepared, SourceGrantScope{
		CompanionID: registry.companionID,
		JobID:       jobID,
		PolicyEpoch: ingest.PolicyEpoch,
	})
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			registry.RevokeJob(jobID)
		}
	}()
	submission := companionIngestSubmission{
		IdempotencyKey:   ingest.IdempotencyKey,
		Request:          ingest.Request,
		SourceCapability: issued.Capability,
	}
	encoded, err := json.Marshal(submission)
	if err != nil {
		return nil, errors.New("encode companion ingest request")
	}
	defer func() {
		for index := range encoded {
			encoded[index] = 0
		}
		issued.Capability = ""
		submission.SourceCapability = ""
	}()
	target := companionAPIPrefix + "/ingest/jobs"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+target, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-PearTube-Job-ID", jobID)
	if err := c.authenticateCompanionRequest(request, encoded); err != nil {
		return nil, err
	}
	response, err := c.companionHTTP.Do(request)
	if err != nil {
		return nil, errors.New("submit companion ingest job")
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return nil, errors.New("companion ingest refused redirect")
	}
	if response.StatusCode != http.StatusAccepted {
		return nil, decodeResponse(response, nil)
	}
	var envelope struct {
		Job companionIngestPublicJob `json:"job"`
	}
	if err := decodeResponse(response, &envelope); err != nil {
		return nil, err
	}
	if envelope.Job.JobID != jobID || envelope.Job.State == "" {
		return nil, errors.New("companion returned a mismatched ingest job")
	}
	accepted = true
	return &ArchiveJob{
		JobID:      jobID,
		Status:     envelope.Job.State,
		EntityHint: companionEntityHint(ingest.Coordinates),
	}, nil
}

// Archive uploads a file to the relay for publication. The body is streamed
// from disk, never buffered: these are whole movies.
func (c *Client) Archive(ctx context.Context, req ArchiveRequest) (*ArchiveJob, error) {
	if c == nil {
		return nil, errors.New("peartube relay is not configured")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	file, err := os.Open(req.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open media file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat media file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", req.FilePath)
	}

	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		writer.CloseWithError(writeArchiveForm(form, file, filepath.Base(req.FilePath), req.ArchiveCoordinates))
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+apiPrefix+"/archive", reader)
	if err != nil {
		reader.CloseWithError(err)
		return nil, err
	}
	httpReq.Header.Set("Content-Type", form.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
		httpReq.Header.Set("Idempotency-Key", key)
	}

	resp, err := c.uploads.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("archive upload: %w", err)
	}
	defer resp.Body.Close()
	var job ArchiveJob
	if err := decodeResponse(resp, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func writeArchiveForm(form *multipart.Writer, file io.Reader, fileName string, req ArchiveCoordinates) error {
	fields := [][2]string{
		{"contentKind", req.ContentKind},
		{"tmdbId", req.TMDBID},
		{"tmdbTitle", req.TMDBTitle},
		{"tmdbPosterPath", req.PosterPath},
		{"tmdbOverview", req.Overview},
		{"tmdbGenres", req.Genres},
	}
	if req.TMDBYear > 0 {
		fields = append(fields, [2]string{"tmdbYear", strconv.Itoa(req.TMDBYear)})
	}
	if req.Runtime > 0 {
		fields = append(fields, [2]string{"tmdbRuntime", strconv.Itoa(req.Runtime)})
	}
	if req.ContentKind == "episode" {
		fields = append(fields,
			[2]string{"tmdbSeason", strconv.Itoa(req.TMDBSeason)},
			[2]string{"tmdbEpisode", strconv.Itoa(req.TMDBEpisode)},
		)
	}
	for _, field := range fields {
		if field[1] == "" {
			continue
		}
		if err := form.WriteField(field[0], field[1]); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("file", fileName)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	return form.Close()
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeResponse(resp, out)
}

// maxErrorBody bounds what we read from a failing relay before giving up on
// finding a structured error in it.
const maxErrorBody = 64 << 10

func decodeResponse(resp *http.Response, out any) error {
	if resp.StatusCode >= 400 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		if err != nil {
			return err
		}
		apiErr := &APIError{Status: resp.StatusCode}
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Field   string `json:"field"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			apiErr.Code = envelope.Error.Code
			apiErr.Message = envelope.Error.Message
			apiErr.Field = envelope.Error.Field
		}
		if apiErr.Message == "" {
			apiErr.Message = strings.TrimSpace(string(body))
		}
		return apiErr
	}
	if out == nil {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode relay response: %w", err)
	}
	return nil
}
