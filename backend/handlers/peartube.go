package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/localmedia"
	"novastream/services/peartube"
)

// localMediaLibrary is the slice of the local media service the seed endpoint
// needs: resolve an item to a file on disk, and prove an explicit path lives
// inside a library the operator configured.
type localMediaLibrary interface {
	GetItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error)
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
}

var _ localMediaLibrary = (*localmedia.Service)(nil)

// streamURLResolver turns an internal stream path this backend handed the player
// (a /debrid/... path, most importantly) into the CDN URL it currently points
// at. debrid.CompositeProvider satisfies it via streaming.DirectURLProvider.
//
// Seeding needs it because the "resolved source URL" a debrid resolve produces
// is not always a URL: Torbox hands back an internal torrent_id:file_id
// reference that only becomes an address at stream time. Re-resolving here also
// sidesteps the short lifetime of the URLs that are addresses (see Seed).
type streamURLResolver interface {
	GetDirectURL(ctx context.Context, path string) (string, error)
}

// tmdbCoordinateResolver recovers the numeric TMDB id the swarm keys a title by,
// for a player that named the title with somebody else's ids.
// metadata.Service satisfies it.
//
// Seeding needs it because no app client here sends a TMDB id: a progress
// heartbeat arrives with itemId `tvdb:movie:343856` and externalIds
// `{imdb, tvdb, titleId}`, and a seed published under no TMDB id is a seed the
// relay rejects. Resolving server-side is what makes every client already in the
// field seed without an app release.
type tmdbCoordinateResolver interface {
	TMDBIDForExternalIDs(ctx context.Context, contentKind string, externalIDs map[string]string, title string, year int) int64
}

// PearTubeHandler exposes the contribution half of the p2p integration: it
// publishes something this viewer can already play into the PearTube swarm, so
// the next viewer can stream it from the swarm instead of from a provider.
//
// Manual contribution and the existing playback trigger share a relay client.
// The playback trigger is inert unless the current versioned policy explicitly
// enables ContributeWatchedMedia.
type PearTubeHandler struct {
	localMedia localMediaLibrary
	streams    streamURLResolver
	// tmdb recovers a TMDB id for a playback that carries none. Nil leaves
	// automatic seeding dependent on clients that send one, which in practice
	// means the browser player only.
	tmdb tmdbCoordinateResolver

	// configMu guards the effective configuration below, which the admin
	// settings page can replace while requests and heartbeats are in flight.
	configMu      sync.RWMutex
	relayPolicyMu sync.Mutex
	// retentionMu makes a settings cutover atomic with the small authenticated
	// companion job handoff. Playback never takes this lock.
	retentionMu sync.RWMutex
	// pendingRelayRevocations retains old relay clients whose authenticated
	// disable failed. retentionMu guards it so a later save cannot falsely
	// succeed without retrying every unresolved remote revocation.
	pendingRelayRevocations []*peartube.Client
	// relay is the client for the configured relay, or nil when this install has
	// no relay at all. Nil is the outer gate for the whole integration.
	relay        *peartube.Client
	sourceGrants *peartube.SourceGrantRegistry
	// contributeWatchedMedia is explicit persisted consent for the existing
	// watch-triggered contribution path.
	contributeWatchedMedia bool
	// archiveOnPlaybackStart makes a consented contribution start with playback
	// and outlive it: the whole title is submitted on the transport start with
	// no watch evidence, and the viewer stopping is no longer a reason to throw
	// the transfer away. It gates nothing on its own.
	archiveOnPlaybackStart bool
	// resolved is the effective relay availability and versioned policy exposed
	// by Status.
	resolved peartube.Resolved
	// relayConsumers are the services that captured the relay client when they
	// were built and therefore have to be handed the new one on a settings save.
	relayConsumers []PearTubeRelayConsumer

	// autoSeedClaims holds the titles an automatic seed has already taken
	// responsibility for, keyed by peartube.EntityKey. See claimAutoSeed.
	autoSeedMu     sync.Mutex
	autoSeedClaims map[string]time.Time

	playbackMu          sync.Mutex
	playbackObserver    *peartube.PlaybackObserver
	playbackNow         func() time.Time
	activeAcquisitions  map[string]*autoSeedAcquisition
	autoSeedJobsByState map[string]int
	autoSeedErrors      []string

	// autoSeedWatchMu guards the relay jobs this process is still answerable
	// for, keyed by relay job id. A job the relay loses mid-transfer can only be
	// revived from here, so the handle outlives the acquisition that made it.
	// See sweepAutoSeedJobs.
	autoSeedWatchMu  sync.Mutex
	autoSeedWatches  map[string]*autoSeedWatch
	autoSeedSweptAt  time.Time
	autoSeedSweeping bool

	// Whether this process is being told about playback at all. Guarded by
	// playbackMu. The designed archive trigger is a playback heartbeat, and the
	// endpoint that delivers one logs nothing, so a client that never reports
	// progress is indistinguishable from archiving being switched off — a whole
	// day of viewing produced 4831 log lines and not one of them was about a
	// playback. Reported at most once a minute, so a per-second heartbeat costs
	// one line.
	playbackSeen       int
	playbackDropped    int
	playbackReportedAt time.Time
}

// The handler is registered on every playback signal this backend produces, and
// each of those signals has its own consumer interface.
var (
	_ playbackAutoSeeder       = (*PearTubeHandler)(nil)
	_ PlaybackActivityObserver = (*PearTubeHandler)(nil)
)

// PearTubeRelayConsumer is a service that captured the relay client when it was
// built and cannot pick up a replacement on its own. indexer.Service satisfies
// it via SetPearTubeRelay.
type PearTubeRelayConsumer interface {
	SetPearTubeRelay(*peartube.Client)
}

// NewPearTubeHandler builds the handler with no relay. The effective
// configuration arrives from ApplyPearTubeSettings, which the caller must invoke
// once at startup with the stored settings.
func NewPearTubeHandler(localMedia *localmedia.Service) *PearTubeHandler {
	handler := &PearTubeHandler{}
	// A typed nil in an interface is not nil; only assign a real service.
	if localMedia != nil {
		handler.localMedia = localMedia
	}
	return handler
}

// AddRelayConsumer registers a service that has to be handed the relay client
// whenever it changes.
func (h *PearTubeHandler) AddRelayConsumer(consumer PearTubeRelayConsumer) {
	if consumer == nil {
		return
	}
	h.configMu.Lock()
	h.relayConsumers = append(h.relayConsumers, consumer)
	relay := h.relay
	h.configMu.Unlock()
	consumer.SetPearTubeRelay(relay)
}

// ApplyPearTubeSettings installs the operator's PearTube configuration on the
// running integration. It is called once at startup and again on every settings
// save, which is what lets an operator point this backend at a relay, move it,
// or switch it off without restarting the container.
//
// One call reconfigures everything: peartube.Configure replaces the process-wide
// client that playback resolution and the media proxy read per use, and the
// services that captured it at build time are handed the new one here. Local
// retention stays fail-closed until the authenticated relay policy accepts the
// cutover, so a failed downgrade can never be reported as successfully applied.
func (h *PearTubeHandler) ApplyPearTubeSettings(stored config.PearTubeSettings) error {
	h.retentionMu.Lock()
	defer h.retentionMu.Unlock()

	resolved := peartube.Resolve(stored)
	relay := peartube.Configure(resolved)
	failClosed := failClosedResolvedPolicy(resolved)

	h.configMu.Lock()
	previousRelay := h.relay
	previousResolved := h.resolved
	h.relay = relay
	h.contributeWatchedMedia = false
	h.resolved = failClosed
	consumers := append([]PearTubeRelayConsumer(nil), h.relayConsumers...)
	h.configMu.Unlock()

	revocationCandidates := append([]*peartube.Client(nil), h.pendingRelayRevocations...)
	h.pendingRelayRevocations = nil
	if previousRelay != nil && previousRelay != relay {
		revocationCandidates = append(revocationCandidates, previousRelay)
	}
	revocations := make([]*peartube.Client, 0, len(revocationCandidates))
	for _, candidate := range revocationCandidates {
		if candidate == nil || candidate == relay {
			continue
		}
		seen := false
		for _, existing := range revocations {
			if existing == candidate {
				seen = true
				break
			}
		}
		if !seen {
			revocations = append(revocations, candidate)
		}
	}
	for _, consumer := range consumers {
		consumer.SetPearTubeRelay(relay)
	}

	policyChanged := previousRelay != relay || previousResolved != resolved
	if policyChanged && (previousResolved.ContributeWatchedMedia || previousResolved.ArchiveEnabled) {
		if h.sourceGrants != nil {
			h.sourceGrants.RevokeAll()
		}
	}
	if previousResolved.ContributeWatchedMedia &&
		(!resolved.ContributeWatchedMedia ||
			resolved.ContributionBudget < previousResolved.ContributionBudget ||
			previousRelay != relay) {
		h.cancelAllAutoSeedAcquisitions()
	}

	policy, policyErr := resolvedPolicySnapshot(resolved)
	if policyErr != nil {
		policy = disabledCompanionPolicy()
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusProbeTimeout)
	defer cancel()
	for index, previous := range revocations {
		if err := h.applyRelayPolicy(ctx, previous, disabledCompanionPolicy()); err != nil {
			h.pendingRelayRevocations = append(h.pendingRelayRevocations, revocations[index:]...)
			h.recordPolicyReconcileFailure()
			return fmt.Errorf("revoke previous peartube relay policy: %w", err)
		}
	}
	if relay != nil {
		if err := h.applyRelayPolicy(ctx, relay, policy); err != nil {
			h.recordPolicyReconcileFailure()
			return fmt.Errorf("apply peartube relay policy: %w", err)
		}
	}
	if policyErr != nil {
		h.recordPolicyReconcileFailure()
		return policyErr
	}

	h.configMu.Lock()
	h.contributeWatchedMedia = resolved.ContributeWatchedMedia
	h.archiveOnPlaybackStart = resolved.ArchiveOnPlaybackStart
	h.resolved = resolved
	h.configMu.Unlock()
	return nil
}

func failClosedResolvedPolicy(resolved peartube.Resolved) peartube.Resolved {
	resolved.ContributeWatchedMedia = false
	resolved.ArchiveEnabled = false
	if !resolved.MigrationRequired && resolved.ConsentVersion == config.PearTubeConsentVersion {
		resolved.EffectiveMode = config.PearTubeModeWatchOnly
	}
	return resolved
}

func disabledCompanionPolicy() peartube.CompanionNetworkPolicy {
	return peartube.CompanionNetworkPolicy{
		PolicyVersion:    companionPolicyVersion,
		ConsentVersion:   companionConsentVersion,
		UploadPermission: "disabled",
	}
}

func (h *PearTubeHandler) recordPolicyReconcileFailure() {
	h.playbackMu.Lock()
	h.recordAutoSeedErrorLocked("POLICY_RECONCILE_FAILED")
	h.playbackMu.Unlock()
}

const (
	companionPolicyVersion  = 2
	companionConsentVersion = 1
	bytesPerGiB             = int64(1024 * 1024 * 1024)
	maxCompanionPolicyBytes = int64(1<<53 - 1)
)

func resolvedPolicySnapshot(resolved peartube.Resolved) (peartube.CompanionNetworkPolicy, error) {
	if resolved.ContributionBudget < 0 || resolved.ArchiveBudget < 0 ||
		int64(resolved.ContributionBudget) > maxCompanionPolicyBytes/bytesPerGiB ||
		int64(resolved.ArchiveBudget) > maxCompanionPolicyBytes/bytesPerGiB {
		return peartube.CompanionNetworkPolicy{}, errors.New("retention budget exceeds companion policy bound")
	}
	contributionBytes := int64(resolved.ContributionBudget) * bytesPerGiB
	archiveBytes := int64(resolved.ArchiveBudget) * bytesPerGiB
	currentConsent := !resolved.MigrationRequired && resolved.ConsentVersion == config.PearTubeConsentVersion
	contribute := currentConsent && resolved.ContributeWatchedMedia
	archive := currentConsent && resolved.ArchiveEnabled
	uploadCeiling := int64(0)
	if contribute {
		uploadCeiling = contributionBytes
	}
	if archive {
		if archiveBytes > maxCompanionPolicyBytes-uploadCeiling {
			return peartube.CompanionNetworkPolicy{}, errors.New("combined retention budget exceeds companion policy bound")
		}
		uploadCeiling += archiveBytes
	}
	uploadPermission := "disabled"
	if contribute || archive {
		uploadPermission = "enabled"
	}
	return peartube.CompanionNetworkPolicy{
		PolicyVersion:           companionPolicyVersion,
		ConsentVersion:          companionConsentVersion,
		MigrationRequired:       false,
		ContributeWatchedMedia:  contribute,
		ArchiveEnabled:          archive,
		ContributionBudgetBytes: contributionBytes,
		ArchiveBudgetBytes:      archiveBytes,
		UploadPermission:        uploadPermission,
		UploadCeilingBytes:      uploadCeiling,
	}, nil
}

func (h *PearTubeHandler) applyRelayPolicy(
	ctx context.Context,
	relay *peartube.Client,
	policy peartube.CompanionNetworkPolicy,
) error {
	if h == nil || relay == nil {
		return errors.New("peartube relay is not configured")
	}
	h.relayPolicyMu.Lock()
	defer h.relayPolicyMu.Unlock()
	return relay.ApplyNetworkPolicy(ctx, policy)
}

func (h *PearTubeHandler) reconcileRelayPolicy(ctx context.Context, relay *peartube.Client) error {
	if h == nil || relay == nil {
		return errors.New("peartube relay is not configured")
	}
	h.configMu.RLock()
	currentRelay, resolved := h.relay, h.resolved
	h.configMu.RUnlock()
	if currentRelay != relay {
		return errors.New("peartube relay configuration changed")
	}
	policy, err := resolvedPolicySnapshot(resolved)
	if err != nil {
		return err
	}
	if err := h.applyRelayPolicy(ctx, relay, policy); err != nil {
		return err
	}
	h.configMu.RLock()
	unchanged := h.relay == relay && h.resolved == resolved
	h.configMu.RUnlock()
	if !unchanged {
		return errors.New("peartube policy changed during reconciliation")
	}
	return nil
}

// currentRelay returns the configured relay client, or nil when there is none.
func (h *PearTubeHandler) currentRelay() *peartube.Client {
	if h == nil {
		return nil
	}
	h.configMu.RLock()
	defer h.configMu.RUnlock()
	return h.relay
}

// SetStreamResolver supplies the provider used to prove that an automatically
// qualified playback has an authenticated local file. Remote results stay
// private playback inputs and are never submitted to the relay.
func (h *PearTubeHandler) SetStreamResolver(streams streamURLResolver) {
	if streams != nil {
		h.streams = streams
	}
}

// SetTMDBResolver supplies the metadata service that recovers a TMDB id from a
// player's IMDb or TVDB ids. Without it, automatic seeding can only publish
// playbacks that already name a TMDB id.
func (h *PearTubeHandler) SetTMDBResolver(tmdb tmdbCoordinateResolver) {
	if tmdb != nil {
		h.tmdb = tmdb
	}
}

// SetSourceGrants installs the process-local authenticated callback registry
// used for local contributions. It is static trusted wiring, not user input.
func (h *PearTubeHandler) SetSourceGrants(registry *peartube.SourceGrantRegistry) {
	h.sourceGrants = registry
}

// SeedRequest names an authenticated local source and its publication
// coordinates. localMediaItemId resolves through the library; filePath must be
// inside a configured library root. url and streamPath are retained only as
// explicit rejection markers so older callers fail closed rather than sending
// a remote locator through the legacy archive path.
type SeedRequest struct {
	LocalMediaItemID string `json:"localMediaItemId,omitempty"`
	FilePath         string `json:"filePath,omitempty"`
	SourceURL        string `json:"url,omitempty"`
	StreamPath       string `json:"streamPath,omitempty"`

	ContentKind    string `json:"contentKind,omitempty"` // "movie" or "episode"
	TMDBID         string `json:"tmdbId,omitempty"`
	TMDBTitle      string `json:"tmdbTitle,omitempty"`
	TMDBYear       int    `json:"tmdbYear,omitempty"`
	TMDBSeason     int    `json:"tmdbSeason,omitempty"`
	TMDBEpisode    int    `json:"tmdbEpisode,omitempty"`
	PosterPath     string `json:"tmdbPosterPath,omitempty"`
	Overview       string `json:"tmdbOverview,omitempty"`
	Runtime        int    `json:"tmdbRuntime,omitempty"`
	Genres         string `json:"tmdbGenres,omitempty"`
	retentionClass string
}

// SeedResponse mirrors the relay's 202 plus the URL to poll.
type SeedResponse struct {
	JobID      string `json:"jobId"`
	Status     string `json:"status"`
	EntityHint string `json:"entityHint"`
	StatusPath string `json:"statusPath"`
}

// statusProbeTimeout bounds the relay round trip a status poll makes. The
// catalog is cached, so a poll right after a search costs nothing.
const statusProbeTimeout = 5 * time.Second

// Status reports relay availability and the effective versioned contribution
// policy independently. It never reports environment configuration as consent.
func (h *PearTubeHandler) Status(w http.ResponseWriter, r *http.Request) {
	h.configMu.RLock()
	relay, resolved := h.relay, h.resolved
	h.configMu.RUnlock()
	h.playbackMu.Lock()
	jobsByState := make(map[string]int, len(h.autoSeedJobsByState))
	for state, count := range h.autoSeedJobsByState {
		jobsByState[state] = count
	}
	lastErrors := append([]string(nil), h.autoSeedErrors...)
	h.playbackMu.Unlock()

	body := map[string]any{
		"enabled":                relay != nil,
		"contributeWatchedMedia": resolved.ContributeWatchedMedia,
		"contributionBudget":     resolved.ContributionBudget,
		"archiveEnabled":         resolved.ArchiveEnabled,
		"archiveBudget":          resolved.ArchiveBudget,
		"budgetUnit":             "GiB",
		"consentVersion":         resolved.ConsentVersion,
		"migrationRequired":      resolved.MigrationRequired,
		"effectiveMode":          resolved.EffectiveMode,
		"fromEnv": map[string]bool{
			"relayUrl": resolved.RelayURLFromEnv,
			"enabled":  resolved.EnabledFromEnv,
		},
		"jobsByState": jobsByState,
		"lastErrors":  lastErrors,
	}
	if relay == nil {
		body["state"] = "disabled"
		writeJSON(w, http.StatusOK, body)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), statusProbeTimeout)
	defer cancel()
	relayState := relay.Probe(ctx)
	// Relay origins and callback coordinates are protected configuration and
	// never belong in status.
	body["reachable"] = relayState.Reachable
	body["notOpen"] = relayState.NotOpen
	body["seedingAvailable"] = relayState.SeedingAvailable
	body["catalogEntities"] = relayState.CatalogEntities
	body["state"] = p2pStateLabel(relayState)
	if relayState.Remedy != "" {
		body["remedy"] = relayState.Remedy
	}
	if relayState.Detail != "" {
		h.playbackMu.Lock()
		h.recordAutoSeedErrorLocked("RELAY_STATUS_ERROR")
		h.playbackMu.Unlock()
	}
	writeJSON(w, http.StatusOK, body)
}

// p2pStateLabel names the relay's condition in one word an admin UI can switch
// on without re-deriving it from the flags.
func p2pStateLabel(state peartube.RelayState) string {
	switch {
	case state.NotOpen:
		return "not_open"
	case !state.Reachable:
		return "unreachable"
	default:
		return "ready"
	}
}

// Seed publishes something this viewer can play into the PearTube swarm.
func (h *PearTubeHandler) Seed(w http.ResponseWriter, r *http.Request) {
	h.configMu.RLock()
	relay, archiveEnabled := h.relay, h.resolved.ArchiveEnabled
	h.configMu.RUnlock()
	if relay == nil {
		writeJSONError(w, "peartube relay is not configured", http.StatusServiceUnavailable)
		return
	}
	if !archiveEnabled {
		writeJSONError(w, "explicit archive consent is required", http.StatusForbidden)
		return
	}
	var req SeedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	submit, err := h.planSeed(r.Context(), relay, req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, localmedia.ErrItemNotFound) {
			status = http.StatusNotFound
		}
		writeJSONError(w, err.Error(), status)
		return
	}

	job, err := submit(r.Context())
	if err != nil {
		// A source the relay will not fetch is the caller's to fix by sending a
		// different one, and says nothing about the relay's health. Anything
		// else — refused for another reason, unreachable, broken — is not.
		var apiErr *peartube.APIError
		if errors.As(err, &apiErr) && peartube.IsSourceRefused(err) {
			message := apiErr.Message
			if message == "" {
				message = apiErr.Error()
			}
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": message,
				"code":  apiErr.Code,
			})
			return
		}
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusAccepted, SeedResponse{
		JobID:      job.JobID,
		Status:     job.Status,
		EntityHint: job.EntityHint,
		StatusPath: "p2p/seed/" + job.JobID,
	})
}

const (
	// autoSeedGuardWindow is how long one title stays claimed by an automatic
	// seed, so a burst of heartbeats, a seek, a stream retry, or two viewers
	// starting the same title cannot enqueue the same whole-file fetch twice.
	//
	// It has to outlast the relay's fetch, because until that finishes the title
	// is not in the catalog and the catalog check cannot see it. Six hours covers
	// a feature-length fetch on a slow link with room to spare, and the claim
	// lapses afterwards, so a seed that failed for a transient reason is retried
	// by a later watch rather than never again.
	autoSeedGuardWindow = 6 * time.Hour

	// autoSeedTimeout bounds one background attempt: a catalog read, a debrid
	// re-resolve, and the relay's acceptance. The relay's own fetch of the file
	// happens after that and is not waited on.
	autoSeedTimeout = 2 * time.Minute

	// autoSeedRetryWindow is how long a claim survives a refusal the relay
	// itself calls transient. Long enough that a heartbeat every few seconds
	// does not resubmit, short enough that the next episode-length watch asks
	// again rather than the title being locked out for the guard window.
	autoSeedRetryWindow = 2 * time.Minute

	// autoSeedSweepInterval bounds how often the relay is asked how the jobs
	// this process is answerable for are doing. Heartbeats arrive every few
	// seconds and each sweep costs one authenticated GET per watched job, so the
	// interval - not the heartbeat rate - is what keeps the check free while
	// nothing is wrong.
	autoSeedSweepInterval = time.Minute
)

// A player's item or series id is namespaced when it came from TMDB
// (`tmdb:movie:603`, `tmdb:tv:1399`, `tmdb:tv:1399:s01e02`). A tvdb- or
// imdb-keyed id carries no TMDB number, and the swarm keys everything by TMDB.
var tmdbPlaybackID = regexp.MustCompile(`(?i)\btmdb:(?:movie|tv|show):([1-9][0-9]{0,9})\b`)

// OnPlaybackStarted handles a transport start.
//
// With archiveOnPlaybackStart on, the start is enough on its own: the whole
// title is submitted from the first signal this playback produces, and nothing
// about the rest of the session is waited on. Off, it stays a zero-evidence
// observation — range opens never qualify by themselves, and only later
// continuous progress can.
func (h *PearTubeHandler) OnPlaybackStarted(update models.PlaybackProgressUpdate) {
	h.observePlayback("", update)
}

// HandlePlaybackUpdate receives the stable playback/source identity matched by
// the stream tracker and never blocks the playback caller. It is the evidence
// half of the same observation: the accumulator that decides whether a playback
// nobody archived on sight has been watched enough to deserve a contribution.
func (h *PearTubeHandler) HandlePlaybackUpdate(userID string, update models.PlaybackProgressUpdate, _ float64) {
	h.observePlayback(userID, update)
}

type autoSeedAcquisition struct {
	cancel    context.CancelFunc
	relay     *peartube.Client
	jobID     string
	state     string
	cancelled bool
	createdAt time.Time
}

// recordPlaybackSignal proves whether this process is being told about playback.
// The archive trigger is a heartbeat, and nothing on that path logged, so a
// client that reports no progress looked exactly like archiving being off.
// Rate-limited to one line a minute, because a heartbeat arrives every few
// seconds per stream and the question this answers is only ever binary.
func (h *PearTubeHandler) recordPlaybackSignal(dropped bool) {
	h.playbackMu.Lock()
	defer h.playbackMu.Unlock()
	h.playbackSeen++
	if dropped {
		h.playbackDropped++
	}
	// Deliberately the wall clock, not the injectable playbackNow: this is a log
	// rate-limiter, and a diagnostic that consumes a tick from a test's injected
	// clock sequence changes the behaviour it was added to observe. It did
	// exactly that once — a fixture handing out a fixed list of times ran off
	// the end of it.
	now := time.Now()
	if !h.playbackReportedAt.IsZero() && now.Sub(h.playbackReportedAt) < time.Minute {
		return
	}
	h.playbackReportedAt = now
	log.Printf("[peartube] playback signals observed: %d (%d carried no source or session)", h.playbackSeen, h.playbackDropped)
}

func (h *PearTubeHandler) observePlayback(userID string, update models.PlaybackProgressUpdate) peartube.PlaybackState {
	if h == nil {
		return peartube.PlaybackUnqualified
	}
	h.configMu.RLock()
	relay, contribute := h.relay, h.contributeWatchedMedia
	archiveOnStart := h.archiveOnPlaybackStart
	h.configMu.RUnlock()
	sourceID := autoSeedSourceID(update.SourcePath)
	playbackID := strings.TrimSpace(update.PlaybackSessionID)
	fallbackPlaybackID := playbackID == ""
	if fallbackPlaybackID && sourceID != "" && strings.TrimSpace(userID) != "" {
		playbackID = autoSeedPlaybackID(userID, update, sourceID)
	}
	h.recordPlaybackSignal(sourceID == "" || playbackID == "")
	if sourceID == "" || playbackID == "" {
		return peartube.PlaybackUnqualified
	}
	if !contribute || relay == nil {
		// Consent is the gate, and this is the only end-of-playback cancel that
		// survives archiveOnPlaybackStart: contribution is off or the relay is
		// gone, so nothing may still be being published on this install's
		// behalf. A withdrawal cancels here whatever the start setting says.
		if update.PlaybackEnded {
			h.cancelAutoSeedAcquisition(playbackID)
		}
		return peartube.PlaybackUnqualified
	}

	// A consented heartbeat is also the cheapest trigger this process has for
	// asking after the archives it already started: it means the relay is
	// configured, consent stands, and somebody is watching something. The check
	// itself is a mutex and a clock read, and the asking happens elsewhere.
	h.maybeSweepAutoSeedJobs()

	h.playbackMu.Lock()
	if h.playbackObserver == nil {
		h.playbackObserver = peartube.NewPlaybackObserver(peartube.PlaybackObserverConfig{})
	}
	now := time.Now()
	if h.playbackNow != nil {
		now = h.playbackNow()
	}
	observer := h.playbackObserver
	h.playbackMu.Unlock()
	observation := observer.Observe(peartube.PlaybackEvent{
		PlaybackID:       playbackID,
		SourceID:         sourceID,
		Position:         playbackSeconds(update.Position),
		Duration:         playbackSeconds(update.Duration),
		ObservedAt:       now,
		Paused:           update.IsPaused,
		Buffering:        update.IsBuffering,
		Abandoned:        update.PlaybackEnded,
		RestartCancelled: fallbackPlaybackID && !update.PlaybackEnded,
		// The operator asked for the whole title, archived when playback
		// starts. The observer's evidence machinery exists to decide whether an
		// unevidenced playback deserves a contribution, and that question is
		// already answered — but its per-source ledger still runs, so a title
		// is submitted once however many signals this playback produces.
		QualifiesImmediately: archiveOnStart,
	})
	// A viewer who stops watching no longer takes the archive with them. The
	// relay pulls its ranges through a grant bound to the job rather than to
	// this session, and an interrupted ingest resumes from its last confirmed
	// block, so an abandoned playback is not a reason to discard confirmed
	// bytes. Consent withdrawal above, an explicit cancel, and the acquisition
	// TTL sweep remain the ways an archive ends early.
	if observation.FirstCancelled && !archiveOnStart {
		h.cancelAutoSeedAcquisition(playbackID)
	}
	if !observation.FirstQualified {
		return observation.State
	}
	plan, ok := h.planAutoSeed(update)
	if !ok {
		return observation.State
	}
	h.startAutoSeedAcquisition(playbackID, plan)
	return observation.State
}

func playbackSeconds(value float64) time.Duration {
	if value <= 0 {
		return 0
	}
	const maxPlaybackSeconds = float64((24 * time.Hour) / time.Second)
	if value > maxPlaybackSeconds {
		value = maxPlaybackSeconds
	}
	return time.Duration(value * float64(time.Second))
}

func autoSeedSourceID(sourcePath string) string {
	normalized := strings.TrimSpace(sourcePath)
	if normalized == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("mediastorm.peartube.playback-source.v1\x00" + normalized))
	return hex.EncodeToString(digest[:])
}

func autoSeedPlaybackID(userID string, update models.PlaybackProgressUpdate, sourceID string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"mediastorm.peartube.playback.v1",
		strings.TrimSpace(userID),
		mediaidentity.NormalizeMediaType(update.MediaType),
		strings.TrimSpace(update.ItemID),
		sourceID,
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

type autoSeedJobCancellation struct {
	relay *peartube.Client
	jobID string
}

func (h *PearTubeHandler) startAutoSeedAcquisition(playbackID string, plan autoSeedPlan) {
	ctx, cancel := context.WithTimeout(context.Background(), autoSeedTimeout)
	acquisition := &autoSeedAcquisition{cancel: cancel, relay: plan.relay, createdAt: time.Now()}
	h.playbackMu.Lock()
	if h.activeAcquisitions == nil {
		h.activeAcquisitions = make(map[string]*autoSeedAcquisition)
	}
	expired := h.expireAutoSeedAcquisitionsLocked(acquisition.createdAt)
	if len(h.activeAcquisitions) >= peartube.DefaultPlaybackObservationCap {
		h.playbackMu.Unlock()
		h.cancelExpiredAutoSeedJobs(expired)
		cancel()
		return
	}
	if _, exists := h.activeAcquisitions[playbackID]; exists {
		h.playbackMu.Unlock()
		h.cancelExpiredAutoSeedJobs(expired)
		cancel()
		return
	}
	h.activeAcquisitions[playbackID] = acquisition
	h.transitionAutoSeedJobStateLocked(acquisition, "queued")
	h.playbackMu.Unlock()
	h.cancelExpiredAutoSeedJobs(expired)

	go func() {
		defer cancel()
		job, err := plan.submitContext(ctx)
		h.playbackMu.Lock()
		h.finishAutoSeedSubmissionLocked(playbackID, acquisition, job, err)
		cancelled := acquisition.cancelled
		jobID := acquisition.jobID
		h.playbackMu.Unlock()
		if cancelled && jobID != "" {
			h.cancelAutoSeedJob(acquisition.relay, jobID)
		}
	}()
}

// expireAutoSeedAcquisitionsLocked frees the tracking slot an aged acquisition
// holds. It does NOT cancel an archive the relay has already accepted.
//
// Age is a statement about how long a PLAYBACK is interesting, and it was the
// wrong clock to cancel an archive by: the relay drives the fetch itself, and a
// feature-length title on a debrid link measured as low as 0.38 MB/s takes far
// longer than any window worth keeping a viewer's observation alive for. A
// relay-side cancel is terminal - it reclaims the staged blocks - so expiring an
// accepted job by age threw away hours of good work and could never finish a
// large title at all.
//
// Dropping our handle does not weaken consent. Withdrawal calls
// sourceGrants.RevokeAll(), which starves every in-flight archive of bytes
// whether we still hold a handle to it or not, so enforcement never depended on
// this map. An accepted job therefore keeps running on the relay's clock, and
// anything that never reached the relay is cancelled here as before, because
// there is nothing to preserve.
func (h *PearTubeHandler) expireAutoSeedAcquisitionsLocked(now time.Time) []autoSeedJobCancellation {
	var jobs []autoSeedJobCancellation
	for playbackID, acquisition := range h.activeAcquisitions {
		if now.Sub(acquisition.createdAt) < peartube.DefaultPlaybackObservationTTL {
			continue
		}
		accepted := acquisition.jobID != ""
		if !accepted {
			acquisition.cancelled = true
			acquisition.cancel()
		}
		h.transitionAutoSeedJobStateLocked(acquisition, "")
		delete(h.activeAcquisitions, playbackID)
	}
	return jobs
}

func (h *PearTubeHandler) cancelExpiredAutoSeedJobs(jobs []autoSeedJobCancellation) {
	for _, job := range jobs {
		go h.cancelAutoSeedJob(job.relay, job.jobID)
	}
}

func (h *PearTubeHandler) finishAutoSeedSubmissionLocked(
	playbackID string,
	acquisition *autoSeedAcquisition,
	job *peartube.ArchiveJob,
	err error,
) {
	if job != nil {
		acquisition.jobID = job.JobID
	}
	if acquisition.cancelled {
		return
	}
	if err != nil {
		if errors.Is(err, errAutoSeedSkipped) || errors.Is(err, context.Canceled) {
			h.transitionAutoSeedJobStateLocked(acquisition, "")
		} else {
			h.transitionAutoSeedJobStateLocked(acquisition, "failed")
			h.recordAutoSeedErrorLocked("CONTRIBUTION_SUBMISSION_FAILED")
		}
		h.removeActiveAutoSeedAcquisitionLocked(playbackID, acquisition)
		return
	}
	if job == nil {
		h.transitionAutoSeedJobStateLocked(acquisition, "failed")
		h.recordAutoSeedErrorLocked("CONTRIBUTION_SUBMISSION_FAILED")
		h.removeActiveAutoSeedAcquisitionLocked(playbackID, acquisition)
		return
	}
	h.transitionAutoSeedJobStateLocked(acquisition, job.Status)
	if isTerminalAutoSeedJobState(job.Status) {
		h.removeActiveAutoSeedAcquisitionLocked(playbackID, acquisition)
	}
}

func (h *PearTubeHandler) cancelAutoSeedAcquisition(playbackID string) {
	h.playbackMu.Lock()
	acquisition := h.activeAcquisitions[playbackID]
	if acquisition == nil || acquisition.cancelled {
		h.playbackMu.Unlock()
		return
	}
	acquisition.cancelled = true
	acquisition.cancel()
	jobID, relay := acquisition.jobID, acquisition.relay
	h.transitionAutoSeedJobStateLocked(acquisition, "cancelled")
	h.removeActiveAutoSeedAcquisitionLocked(playbackID, acquisition)
	h.playbackMu.Unlock()
	if jobID != "" {
		go h.cancelAutoSeedJob(relay, jobID)
	}
}

func (h *PearTubeHandler) cancelAllAutoSeedAcquisitions() {
	h.playbackMu.Lock()
	ids := make([]string, 0, len(h.activeAcquisitions))
	for playbackID := range h.activeAcquisitions {
		ids = append(ids, playbackID)
	}
	h.playbackMu.Unlock()
	for _, playbackID := range ids {
		h.cancelAutoSeedAcquisition(playbackID)
	}
	// Nothing may still be published on this install's behalf, so the revival
	// handles go too - including the ones whose acquisition already aged out of
	// the tracking map, which the loop above cannot reach.
	h.forgetAllAutoSeedJobs()
}

func (h *PearTubeHandler) cancelAutoSeedJob(relay *peartube.Client, jobID string) {
	// A job cancelled on purpose is not a job to revive.
	h.forgetAutoSeedJob(jobID)
	ctx, cancel := context.WithTimeout(context.Background(), statusProbeTimeout)
	defer cancel()
	if err := relay.CancelArchive(ctx, jobID); err != nil {
		h.playbackMu.Lock()
		h.recordAutoSeedErrorLocked("CONTRIBUTION_CANCEL_FAILED")
		h.playbackMu.Unlock()
	}
}

func (h *PearTubeHandler) transitionAutoSeedJobStateLocked(acquisition *autoSeedAcquisition, state string) {
	previous := acquisition.state
	next := ""
	if strings.TrimSpace(state) != "" {
		next = normalizeAutoSeedJobState(state)
	}
	if previous == next {
		return
	}
	if previous != "" {
		h.bumpAutoSeedJobStateLocked(previous, -1)
	}
	acquisition.state = next
	if next != "" {
		h.bumpAutoSeedJobStateLocked(next, 1)
	}
}

func (h *PearTubeHandler) removeActiveAutoSeedAcquisitionLocked(
	playbackID string,
	acquisition *autoSeedAcquisition,
) {
	if h.activeAcquisitions[playbackID] == acquisition {
		delete(h.activeAcquisitions, playbackID)
	}
}

func isTerminalAutoSeedJobState(state string) bool {
	switch normalizeAutoSeedJobState(state) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func normalizeAutoSeedJobState(state string) string {
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		return "unknown"
	}
	return state
}

func (h *PearTubeHandler) bumpAutoSeedJobStateLocked(state string, delta int) {
	if h.autoSeedJobsByState == nil {
		h.autoSeedJobsByState = make(map[string]int)
	}
	state = normalizeAutoSeedJobState(state)
	h.autoSeedJobsByState[state] += delta
	if h.autoSeedJobsByState[state] <= 0 {
		delete(h.autoSeedJobsByState, state)
	}
}

func (h *PearTubeHandler) recordAutoSeedErrorLocked(code string) {
	h.autoSeedErrors = append(h.autoSeedErrors, code)
	if len(h.autoSeedErrors) > 8 {
		h.autoSeedErrors = h.autoSeedErrors[len(h.autoSeedErrors)-8:]
	}
}

// autoSeedWatch is one job the relay accepted, together with the plan that can
// re-drive it.
//
// The plan is retained rather than rebuilt because reviving a job means issuing
// a fresh capability for the SAME source under the SAME idempotency key: the
// relay hashes the job id out of the request, so re-driving an unchanged plan
// lands on the job that already holds the confirmed bytes instead of starting
// the title again from zero.
//
// The map is in memory only. A MediaStorm restart therefore forgets what it was
// answerable for, and the title's claim lapsing is what lets a later watch
// resubmit - which resumes just the same, because the job id is derived, not
// remembered.
type autoSeedWatch struct {
	plan autoSeedPlan
	// redrivenFrom is the confirmed offset the current round of attempts is
	// being spent on, attempts how many of them have been spent there, and
	// landed whether one of them actually reached the relay. Bytes moving past
	// redrivenFrom start a fresh round; nothing else does. See
	// reviveAutoSeedJob.
	redrivenFrom int64
	attempts     int
	landed       bool
}

// maxAutoSeedRedriveAttempts bounds how many times one confirmed offset may be
// asked about when no submission has landed there yet. A sweep happens at most
// once per autoSeedSweepInterval, so three attempts spans about three minutes -
// long enough to outlast a relay restart or an update, short enough that a relay
// refusing forever costs three round trips and then silence. A submission that
// does land spends the offset outright: failing again at bytes a re-drive
// already ran from is a loop with a known outcome.
const maxAutoSeedRedriveAttempts = 3

// watchAutoSeedJob makes an accepted job answerable, so a transfer the relay
// loses can be resumed rather than abandoned.
func (h *PearTubeHandler) watchAutoSeedJob(jobID string, plan autoSeedPlan) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	h.autoSeedWatchMu.Lock()
	defer h.autoSeedWatchMu.Unlock()
	if h.autoSeedWatches == nil {
		h.autoSeedWatches = make(map[string]*autoSeedWatch)
	}
	if existing := h.autoSeedWatches[jobID]; existing != nil {
		// A resubmission of the same job takes the fresher plan but keeps the
		// re-drive bookkeeping: it IS a revival, so forgetting how many this job
		// has already had is how a stalled title starts costing round trips
		// forever.
		existing.plan = plan
		return
	}
	if len(h.autoSeedWatches) >= peartube.DefaultPlaybackObservationCap {
		return
	}
	h.autoSeedWatches[jobID] = &autoSeedWatch{plan: plan}
}

func (h *PearTubeHandler) forgetAutoSeedJob(jobID string) {
	h.autoSeedWatchMu.Lock()
	defer h.autoSeedWatchMu.Unlock()
	delete(h.autoSeedWatches, jobID)
}

func (h *PearTubeHandler) forgetAllAutoSeedJobs() {
	h.autoSeedWatchMu.Lock()
	defer h.autoSeedWatchMu.Unlock()
	h.autoSeedWatches = nil
}

// maybeSweepAutoSeedJobs runs the sweep off the caller's goroutine, at most once
// per autoSeedSweepInterval and never when there is nothing to ask about. The
// caller is a playback heartbeat, so the decision has to cost a mutex and a
// clock read and nothing else.
func (h *PearTubeHandler) maybeSweepAutoSeedJobs() {
	now := time.Now()
	h.autoSeedWatchMu.Lock()
	if len(h.autoSeedWatches) == 0 || h.autoSeedSweeping ||
		now.Sub(h.autoSeedSweptAt) < autoSeedSweepInterval {
		h.autoSeedWatchMu.Unlock()
		return
	}
	h.autoSeedSweeping = true
	h.autoSeedSweptAt = now
	h.autoSeedWatchMu.Unlock()
	go func() {
		defer func() {
			h.autoSeedWatchMu.Lock()
			h.autoSeedSweeping = false
			h.autoSeedWatchMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), autoSeedTimeout)
		defer cancel()
		h.sweepAutoSeedJobs(ctx)
	}()
}

// sweepAutoSeedJobs asks the relay how each job this process is answerable for
// is doing, and re-drives the ones that ended in a way a fresh capability
// answers.
//
// This is the half MediaStorm never had. A relay killed mid-transfer - by an
// update, a crash, or restart=on-failure - marks the job failed and recoverable,
// keeps every confirmed byte, and waits for a capability whose grant lifetime is
// far shorter than the archive it was serving. Only this process can issue one.
// Nothing asked, so those bytes sat there while the title's own claim blocked
// any resubmission: three archives died that way in one day, at 528, 448 and 100
// MiB of real transferred bytes.
func (h *PearTubeHandler) sweepAutoSeedJobs(ctx context.Context) {
	h.autoSeedWatchMu.Lock()
	jobIDs := make([]string, 0, len(h.autoSeedWatches))
	for jobID := range h.autoSeedWatches {
		jobIDs = append(jobIDs, jobID)
	}
	h.autoSeedWatchMu.Unlock()
	for _, jobID := range jobIDs {
		if ctx.Err() != nil {
			return
		}
		h.reviveAutoSeedJob(ctx, jobID)
	}
}

// reviveAutoSeedJob decides what one watched job deserves, and is where every
// guard against a resubmit loop lives.
func (h *PearTubeHandler) reviveAutoSeedJob(ctx context.Context, jobID string) {
	h.autoSeedWatchMu.Lock()
	watch := h.autoSeedWatches[jobID]
	h.autoSeedWatchMu.Unlock()
	if watch == nil {
		return
	}
	job, err := watch.plan.relay.IngestJob(ctx, jobID)
	if err != nil {
		// A relay that cannot be asked is not a relay that lost the job. Nothing
		// is submitted and nothing is forgotten; the next sweep asks again.
		return
	}
	if normalizeAutoSeedJobState(job.State) != "failed" {
		// A job still working is never resubmitted: its bytes are already
		// moving, and a second submission would only race the first. A finished
		// one has nothing left to answer for.
		if isTerminalAutoSeedJobState(job.State) {
			h.forgetAutoSeedJob(jobID)
			h.sourceGrants.RevokeJob(jobID)
		}
		return
	}
	if !job.Recoverable {
		// The relay is saying the staged bytes cannot become the title this job
		// asked for - a length or identity disagreement, or consent withdrawn.
		// No capability this process can issue changes that, and the claim stays
		// held for the guard window exactly as a refused seed's does.
		log.Printf("[peartube] autoseed %s: job %s failed unrecoverably (%s), not re-driving",
			watch.plan.key, jobID, job.ErrorCode)
		h.forgetAutoSeedJob(jobID)
		h.sourceGrants.RevokeJob(jobID)
		return
	}
	h.autoSeedWatchMu.Lock()
	if h.autoSeedWatches[jobID] != watch {
		h.autoSeedWatchMu.Unlock()
		return
	}
	if watch.attempts > 0 && job.BytesReceived <= watch.redrivenFrom {
		// No progress since the last round of attempts. A landed submission has
		// already had its chance at these bytes, and an unlanded one only gets
		// maxAutoSeedRedriveAttempts of them; either way the handle goes, so
		// this job is never asked about again. Confirmed bytes moving forward
		// means the last attempt did real work and the next one starts further
		// along, so a feature-length title survives any number of
		// interruptions - but progress, not the clock, is what licenses it.
		if watch.landed || watch.attempts >= maxAutoSeedRedriveAttempts {
			h.autoSeedWatchMu.Unlock()
			log.Printf("[peartube] autoseed %s: job %s failed again at %d bytes (%s) after %d attempt(s), abandoning the resume",
				watch.plan.key, jobID, job.BytesReceived, job.ErrorCode, watch.attempts)
			h.forgetAutoSeedJob(jobID)
			return
		}
	} else {
		watch.attempts = 0
		watch.landed = false
	}
	watch.redrivenFrom = job.BytesReceived
	watch.attempts++
	h.autoSeedWatchMu.Unlock()

	log.Printf("[peartube] autoseed %s: re-driving job %s from %d/%d confirmed bytes (%s)",
		watch.plan.key, jobID, job.BytesReceived, job.ExpectedBytes, job.ErrorCode)
	revived, err := watch.plan.redrive(ctx)
	if err != nil {
		h.playbackMu.Lock()
		h.recordAutoSeedErrorLocked("CONTRIBUTION_REDRIVE_FAILED")
		h.playbackMu.Unlock()
		log.Printf("[peartube] autoseed %s: re-drive of job %s refused: %v", watch.plan.key, jobID, err)
		// The same distinction a first submission makes. A relay that was merely
		// not ready has not answered the question, so the next sweep - a whole
		// interval later, never a hot loop - asks again, until this offset's
		// attempts are spent. The attempt itself is NOT given back: that is what
		// turned a relay refusing forever into one resubmission per minute for
		// the life of the process. A refusal the relay decided on stands, and
		// the handle goes now.
		if !autoSeedRefusalIsTransient(err) {
			h.forgetAutoSeedJob(jobID)
		}
		return
	}
	h.autoSeedWatchMu.Lock()
	if h.autoSeedWatches[jobID] == watch {
		// A submission reached the relay for these bytes. Nothing but progress
		// past them licenses another.
		watch.landed = true
	}
	h.autoSeedWatchMu.Unlock()
	if revived.JobID != jobID {
		// The source no longer hashes to the job holding the bytes, so nothing
		// was resumed - a genuinely different source under the same title. Watch
		// what was actually accepted instead.
		log.Printf("[peartube] autoseed %s: re-drive of job %s landed on %s, the staged bytes were not resumed",
			watch.plan.key, jobID, revived.JobID)
		h.forgetAutoSeedJob(jobID)
		h.watchAutoSeedJob(revived.JobID, watch.plan)
		return
	}
	log.Printf("[peartube] autoseed %s: job %s resumed from %d bytes (%s)",
		watch.plan.key, jobID, job.BytesReceived, revived.Status)
}

// autoSeedPlan is an automatic seed that has claimed its title and is waiting to
// be submitted off the request path.
type autoSeedPlan struct {
	handler *PearTubeHandler
	// relay is the client the plan was made against, carried rather than
	// re-read: a settings save could repoint the relay between the claim and the
	// submission, and this seed belongs to the relay whose catalog it checked.
	relay   *peartube.Client
	request SeedRequest
	// key is the claim this plan holds and the name it logs under: the swarm's
	// EntityKey once the title has a TMDB id, and the playback's own identity
	// until then.
	key string
	// pendingKey is the pre-resolution claim, retained alongside the entity key
	// so a plan that has to be abandoned releases both.
	pendingKey string
	// update is carried so the TMDB id can be recovered off the request path.
	update models.PlaybackProgressUpdate
}

// planAutoSeed decides, without touching the network, whether this heartbeat
// should become a seed — and claims the title if so, so that the decision is
// made once even when heartbeats overlap.
func (h *PearTubeHandler) planAutoSeed(update models.PlaybackProgressUpdate) (autoSeedPlan, bool) {
	if h == nil {
		return autoSeedPlan{}, false
	}
	// Every refusal below used to be silent, and this runs at most once per source
	// per observation TTL - it is only reached once a playback has qualified - so
	// naming the reason costs one line per title rather than one per heartbeat.
	h.configMu.RLock()
	relay, contribute := h.relay, h.contributeWatchedMedia
	h.configMu.RUnlock()
	if relay == nil {
		log.Printf("[peartube] autoseed skipped: no relay configured")
		return autoSeedPlan{}, false
	}
	if !contribute {
		log.Printf("[peartube] autoseed skipped: contributeWatchedMedia is off")
		return autoSeedPlan{}, false
	}
	request, ok := autoSeedRequest(update)
	if !ok {
		log.Printf("[peartube] autoseed skipped: playback carries nothing to publish under (sourcePath=%q mediaType=%q itemId=%q)",
			update.SourcePath, update.MediaType, update.ItemID)
		return autoSeedPlan{}, false
	}
	// A playback that already names a TMDB id is claimed by the swarm's own key,
	// exactly as before. An app client names the title by TVDB or IMDb instead,
	// and recovering the TMDB number is a lookup that must not happen on the
	// player's request path — so the claim is taken on the identity the playback
	// does have, and identified promotes it once the number lands.
	plan := autoSeedPlan{handler: h, relay: relay, request: request, update: update}
	plan.key = peartube.EntityKey(seedCoordinates(request))
	if plan.key == "" {
		plan.pendingKey = autoSeedPendingKey(update, request)
		plan.key = plan.pendingKey
	}
	if plan.key == "" {
		log.Printf("[peartube] autoseed skipped: no swarm key for %q", request.TMDBTitle)
		return autoSeedPlan{}, false
	}
	if !h.claimAutoSeed(plan.key) {
		log.Printf("[peartube] autoseed skipped: %s is already claimed by an attempt in this guard window", plan.key)
		return autoSeedPlan{}, false
	}
	return plan, true
}

// autoSeedRequest turns a playback heartbeat into the seed request the manual
// endpoint would have received, or reports that this playback is not seedable.
//
// It names the stream path and never a URL. The seed path re-resolves that path
// server-side, which is the only thing that works: a Torbox resolution is an
// internal torrent_id:file_id reference rather than an address, and the debrid
// URLs that are addresses expire in about ten minutes.
//
// The TMDB id may come back empty. Every app client here identifies titles by
// TVDB and IMDb and sends no TMDB number at all, so requiring one at this point
// silently discarded every app playback ever made; it is recovered later, off
// this path. Everything else the relay insists on is settled here.
func autoSeedRequest(update models.PlaybackProgressUpdate) (SeedRequest, bool) {
	request := SeedRequest{StreamPath: strings.TrimSpace(update.SourcePath)}
	if request.StreamPath == "" {
		// Nothing to re-resolve, and the player's own URL must never be
		// forwarded in its place.
		return SeedRequest{}, false
	}

	switch mediaidentity.NormalizeMediaType(update.MediaType) {
	case "movie":
		request.ContentKind = "movie"
		request.TMDBID = tmdbIDFromPlayback(update.ItemID, update.ExternalIDs)
		request.TMDBTitle = strings.TrimSpace(update.MovieName)
		request.TMDBYear = update.Year
	case "episode":
		// The swarm keys an episode by its series' TMDB id, which is what
		// seriesId carries; an episode's own id is no use here.
		seriesID := strings.TrimSpace(update.SeriesID)
		if seriesID == "" {
			seriesID = update.ItemID
		}
		request.ContentKind = "episode"
		request.TMDBID = tmdbIDFromPlayback(seriesID, update.ExternalIDs)
		request.TMDBTitle = strings.TrimSpace(update.SeriesName)
		request.TMDBSeason = update.SeasonNumber
		request.TMDBEpisode = update.EpisodeNumber
	default:
		// Live TV and anything else has no TMDB coordinates to publish under.
		return SeedRequest{}, false
	}

	// The artwork the player is already showing. A consumer of the swarm holds
	// no TMDB credentials and cannot look a cover up, so a publication seeded
	// without one renders as a blank card on every peer that ever sees it. The
	// relay fetches the image itself from the path, which is why the URL is not
	// forwarded as-is.
	request.PosterPath = tmdbPosterPath(update.PosterURL)

	// Everything the relay requires except the TMDB id is checked now, against a
	// stand-in number, so a playback that could never be published — no title to
	// publish under, an episode with no season or episode number — is dropped
	// before anything is claimed or looked up.
	probe := seedCoordinates(request)
	if probe.TMDBID == "" {
		probe.TMDBID = "1"
	}
	if err := probe.Validate(); err != nil {
		return SeedRequest{}, false
	}
	return request, true
}

// tmdbPosterPath recovers the provider's own artwork path from a poster URL
// this server built, e.g. https://image.tmdb.org/t/p/w780/abc.jpg -> /abc.jpg.
//
// The relay takes a path rather than a URL on purpose: it fetches the image
// itself, from an origin it chose, so a caller cannot point a publisher at an
// arbitrary host. Anything that is not TMDB artwork - a local placeholder, a
// proxied thumbnail, another provider - yields "" and the seed simply carries
// no cover, which is the honest outcome.
func tmdbPosterPath(posterURL string) string {
	raw := strings.TrimSpace(posterURL)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Host, "image.tmdb.org") {
		return ""
	}
	// /t/p/<size>/<file>: the size is this server's rendering choice, and the
	// relay picks its own, so only the file name travels.
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "t" || parts[1] != "p" {
		return ""
	}
	file := strings.TrimSpace(parts[3])
	if file == "" || strings.ContainsAny(file, `\/`) {
		return ""
	}
	return "/" + file
}

// autoSeedPendingKey names a title by whatever identity the player did supply, so
// one claim can cover a heartbeat storm before the TMDB id is known. It only has
// to be stable and unique per title; the swarm's own key takes over as soon as
// the lookup lands.
func autoSeedPendingKey(update models.PlaybackProgressUpdate, request SeedRequest) string {
	ids := mediaidentity.NormalizeExternalIDs(update.ExternalIDs)
	identity := ""
	for _, candidate := range []string{
		ids["imdb"], ids["tvdb"], update.SeriesID, update.ItemID,
	} {
		if identity = strings.TrimSpace(candidate); identity != "" {
			break
		}
	}
	if identity == "" {
		return ""
	}
	if request.ContentKind == "episode" {
		return fmt.Sprintf("pending:show:%s:s%d:e%d", identity, request.TMDBSeason, request.TMDBEpisode)
	}
	return "pending:movie:" + identity
}

// tmdbIDFromPlayback recovers the numeric TMDB id a seed must be published
// under. The external id map wins: for an episode its `tmdb` entry is the series
// id, which is exactly the coordinate needed, and it is present even when the
// player's own ids came from another provider.
func tmdbIDFromPlayback(id string, externalIDs map[string]string) string {
	if value := strings.TrimSpace(mediaidentity.NormalizeExternalIDs(externalIDs)["tmdb"]); isTMDBID(value) {
		return value
	}
	if match := tmdbPlaybackID.FindStringSubmatch(id); match != nil {
		return match[1]
	}
	return ""
}

// claimAutoSeed reserves a title for one automatic seed, reporting whether the
// caller got it. This is the in-process half of the dedupe; the relay catalog is
// the other half, and only this half can stop two submissions racing each other
// before either reaches the relay.
func (h *PearTubeHandler) claimAutoSeed(key string) bool {
	now := time.Now()
	h.autoSeedMu.Lock()
	defer h.autoSeedMu.Unlock()
	if h.autoSeedClaims == nil {
		h.autoSeedClaims = make(map[string]time.Time)
	}
	if until, held := h.autoSeedClaims[key]; held && until.After(now) {
		return false
	}
	// Lapsed claims are dropped here rather than by a timer: the map only grows
	// when something is played, so the sweep runs exactly as often as it needs to.
	for claimed, until := range h.autoSeedClaims {
		if !until.After(now) {
			delete(h.autoSeedClaims, claimed)
		}
	}
	h.autoSeedClaims[key] = now.Add(autoSeedGuardWindow)
	return true
}

func (h *PearTubeHandler) releaseAutoSeed(key string) {
	h.autoSeedMu.Lock()
	defer h.autoSeedMu.Unlock()
	delete(h.autoSeedClaims, key)
}

// shortenAutoSeed pulls a claim's expiry in without dropping it. A relay that
// was not ready should be asked again soon, but not on the very next playback
// heartbeat: those arrive seconds apart, and one refusal per guard window is the
// right amount of noise.
func (h *PearTubeHandler) shortenAutoSeed(key string, until time.Time) {
	h.autoSeedMu.Lock()
	defer h.autoSeedMu.Unlock()
	if held, ok := h.autoSeedClaims[key]; ok && held.After(until) {
		h.autoSeedClaims[key] = until
	}
}

// releaseClaims drops everything this plan claimed, so a later watch asks again.
func (p autoSeedPlan) releaseClaims() {
	p.handler.releaseAutoSeed(p.key)
	if p.pendingKey != "" && p.pendingKey != p.key {
		p.handler.releaseAutoSeed(p.pendingKey)
	}
}

// shortenClaims keeps this plan's claims but makes them lapse soon, so a relay
// that was merely not ready is retried by a later watch instead of being locked
// out for the whole guard window.
//
// It also releases the source's qualification, because that is the stricter of
// the two guards and shortening the claim alone achieves nothing: qualification
// is once per source per six hours, so no later heartbeat would reach the
// attempt the shortened claim was making room for. Thor Ragnarok proved it —
// claim shortened to two minutes, and five minutes later nothing had retried.
func (p autoSeedPlan) shortenClaims(now time.Time) {
	until := now.Add(autoSeedRetryWindow)
	p.handler.shortenAutoSeed(p.key, until)
	if p.pendingKey != "" && p.pendingKey != p.key {
		p.handler.shortenAutoSeed(p.pendingKey, until)
	}
	p.handler.forgetQualifiedSource(autoSeedSourceID(p.update.SourcePath))
}

// forgetQualifiedSource lets the next heartbeat qualify this source again. Guards
// the observer's own nil case, because a handler with no relay never built one.
func (h *PearTubeHandler) forgetQualifiedSource(sourceID string) {
	if h == nil || sourceID == "" {
		return
	}
	h.playbackMu.Lock()
	observer := h.playbackObserver
	h.playbackMu.Unlock()
	observer.ForgetQualifiedSource(sourceID)
}

// identified supplies the TMDB id the swarm keys on when the player named the
// title some other way, and returns the plan re-keyed onto the swarm's identity.
//
// The lookup lives here, on the submission goroutine, because it can reach TMDB
// and the caller is a playback heartbeat. The metadata layer memoises it in its
// long-lived id cache, so a title costs one lookup on first play and nothing
// afterwards.
func (p autoSeedPlan) identified(ctx context.Context) (autoSeedPlan, bool) {
	if p.pendingKey == "" {
		return p, true
	}
	var tmdbID int64
	if p.handler.tmdb != nil {
		tmdbID = p.handler.tmdb.TMDBIDForExternalIDs(
			ctx, p.request.ContentKind, p.update.ExternalIDs, p.request.TMDBTitle, p.request.TMDBYear)
	}
	if tmdbID <= 0 {
		// The claim is kept: one line per title per guard window. Reporting this
		// at all is the point — while it was silent, "the trigger never fired"
		// and "the trigger fired and could not name the title" were
		// indistinguishable from outside the process.
		log.Printf("[peartube] autoseed %s: no tmdb id for %q (%s), not seeding",
			p.key, p.request.TMDBTitle, autoSeedIDsForLog(p.update))
		return autoSeedPlan{}, false
	}
	p.request.TMDBID = strconv.FormatInt(tmdbID, 10)
	key := peartube.EntityKey(seedCoordinates(p.request))
	if key == "" {
		log.Printf("[peartube] autoseed %s: tmdb id %d does not name a publishable entity, not seeding",
			p.key, tmdbID)
		return autoSeedPlan{}, false
	}
	// Claim the swarm's identity too, so a second client that does name this
	// title by TMDB id cannot seed it again. The pending claim stays held, which
	// is what stops this lookup being repeated for the rest of the window.
	if !p.handler.claimAutoSeed(key) {
		return autoSeedPlan{}, false
	}
	p.key = key
	log.Printf("[peartube] autoseed %s: recovered tmdb id %d for %q from %s",
		key, tmdbID, p.request.TMDBTitle, autoSeedIDsForLog(p.update))
	return p, true
}

// autoSeedIDsForLog names the ids an identification had to work with, so a line
// about a title that could not be named says which lookup to go and fix.
func autoSeedIDsForLog(update models.PlaybackProgressUpdate) string {
	ids := mediaidentity.NormalizeExternalIDs(update.ExternalIDs)
	parts := make([]string, 0, 5)
	if itemID := strings.TrimSpace(update.ItemID); itemID != "" {
		parts = append(parts, "itemId="+itemID)
	}
	if seriesID := strings.TrimSpace(update.SeriesID); seriesID != "" {
		parts = append(parts, "seriesId="+seriesID)
	}
	for _, name := range [...]string{"imdb", "tvdb", "tmdb"} {
		if value := strings.TrimSpace(ids[name]); value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	if len(parts) == 0 {
		return "no ids at all"
	}
	return strings.Join(parts, " ")
}

var (
	errAutoSeedSkipped           = errors.New("automatic contribution skipped")
	errAutoSeedSourceUnavailable = errors.New("automatic contribution source unavailable")
)

func (p autoSeedPlan) submit() {
	ctx, cancel := context.WithTimeout(context.Background(), autoSeedTimeout)
	defer cancel()
	_, _ = p.submitContext(ctx)
}

// autoSeedRefusalIsTransient reports whether the relay's refusal was about its
// own readiness rather than about this request. Anything that is not a decision
// the relay made about the submission - a transport failure, a timeout, a 5xx -
// is worth asking again on the next watch.
func autoSeedRefusalIsTransient(err error) bool {
	var apiErr *peartube.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 500 || apiErr.Status == http.StatusTooManyRequests ||
			apiErr.Status == http.StatusRequestTimeout
	}
	// No HTTP answer at all: the relay was unreachable or the request died in
	// flight, which says nothing about whether the title is seedable.
	return true
}

func (p autoSeedPlan) submitContext(ctx context.Context) (*peartube.ArchiveJob, error) {
	p, ok := p.identified(ctx)
	if !ok {
		return nil, errAutoSeedSkipped
	}

	published, err := p.relay.CatalogHasEntity(ctx, seedCoordinates(p.request))
	switch {
	case err != nil:
		p.releaseClaims()
		log.Printf("[peartube] autoseed %s: skipped, catalog unavailable: %v", p.key, err)
		return nil, err
	case published:
		log.Printf("[peartube] autoseed %s: already served by the swarm", p.key)
		return nil, errAutoSeedSkipped
	}

	submit, err := p.handler.planQualifiedAutoSeed(ctx, p.relay, p.request)
	if err != nil {
		if errors.Is(err, errAutoSeedSourceUnavailable) {
			p.handler.playbackMu.Lock()
			p.handler.recordAutoSeedErrorLocked("CONTRIBUTION_SOURCE_UNAVAILABLE")
			p.handler.playbackMu.Unlock()
			// This used to hold the claim for the whole guard window, because
			// "source unavailable" once meant a source that could never be
			// contributed. It no longer does: remote sources archive through a
			// grant now, so the remaining reasons are all failures to RESOLVE a
			// path - which the next watch may well resolve. Holding the claim
			// locked such a title out for six hours; one that only just became
			// seedable stayed refused long after the cause was fixed.
			p.shortenClaims(time.Now())
		} else {
			p.releaseClaims()
		}
		log.Printf("[peartube] autoseed %s: not seedable: %v", p.key, err)
		return nil, err
	}
	job, err := submit(ctx)
	if err != nil {
		// A source the relay genuinely refuses keeps its claim: it would be
		// refused again on the next heartbeat, and one log line per guard window
		// is the right amount of noise.
		//
		// A relay that was merely NOT READY is a different thing, and holding the
		// claim through it locked the title out for the whole six-hour window.
		// Watching again is the only retry trigger this design has, so a restart
		// or a policy not yet re-pushed made a title silently unarchivable for
		// the rest of the day. Observed live: a seed arrived nine seconds before
		// the relay finished restarting, and replaying the episode did nothing.
		//
		// The relay already says which it is. 5xx and transport failures are
		// "ask me again"; a 4xx is a decision about this request that a retry
		// cannot change.
		if autoSeedRefusalIsTransient(err) {
			p.shortenClaims(time.Now())
		}
		log.Printf("[peartube] autoseed %s: relay refused the seed: %v", p.key, err)
		return nil, err
	}
	log.Printf("[peartube] autoseed %s: relay accepted job %s (%s)", p.key, job.JobID, job.Status)
	// From here the relay owns the transfer, and this process owns the only thing
	// that can revive it if the relay loses it: a fresh capability for the same
	// source. Keep the handle.
	p.handler.watchAutoSeedJob(job.JobID, p)
	return job, nil
}

// redrive resubmits a job the relay accepted and then lost, resuming from the
// bytes it confirmed instead of starting the title again.
//
// It deliberately skips the checks submitContext makes rather than calling it.
// Identification already happened; the catalog check would be answered by the
// very relay still holding this title's staged bytes; and the claim is the one
// this plan already holds, so re-taking it would fail and abandon the revival.
// What matters is that the request is unchanged, because the relay hashes the
// job id out of it - an unchanged request lands on the job with the bytes.
func (p autoSeedPlan) redrive(ctx context.Context) (*peartube.ArchiveJob, error) {
	submit, err := p.handler.planQualifiedAutoSeed(ctx, p.relay, p.request)
	if err != nil {
		return nil, err
	}
	return submit(ctx)
}

// seedCoordinates are the TMDB coordinates a seed request publishes under.
// Shared with the automatic trigger, which needs them before it commits to a
// seed in order to ask the relay whether the swarm already has this title.
func seedCoordinates(req SeedRequest) peartube.ArchiveCoordinates {
	return peartube.ArchiveCoordinates{
		ContentKind: strings.TrimSpace(req.ContentKind),
		TMDBID:      strings.TrimSpace(req.TMDBID),
		TMDBTitle:   strings.TrimSpace(req.TMDBTitle),
		TMDBYear:    req.TMDBYear,
		TMDBSeason:  req.TMDBSeason,
		TMDBEpisode: req.TMDBEpisode,
		PosterPath:  strings.TrimSpace(req.PosterPath),
		Overview:    strings.TrimSpace(req.Overview),
		Runtime:     req.Runtime,
		Genres:      strings.TrimSpace(req.Genres),
	}
}

// seedIdempotencyKey binds one logical media source to one durable relay job.
// The source identity is the stable MediaStorm path/item, never a short-lived
// debrid CDN URL. Hashing keeps provider tokens and local paths out of headers
// and relay metadata.
func seedIdempotencyKey(coordinates peartube.ArchiveCoordinates, sourceIdentity string) string {
	parts := []string{
		"mediastorm.seed.v1",
		strings.TrimSpace(coordinates.ContentKind),
		strings.TrimSpace(coordinates.TMDBID),
		strconv.Itoa(coordinates.TMDBSeason),
		strconv.Itoa(coordinates.TMDBEpisode),
		strings.TrimSpace(sourceIdentity),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "mediastorm-v1_" + hex.EncodeToString(sum[:])
}

// planQualifiedAutoSeed resolves the playback's stream path server-side and
// picks the seed transport that source needs.
//
// normalizeAutoSeedStreamPath strips the WebDAV prefix a resolved playback hands
// back, because the streaming providers are keyed on the path underneath it.
// main.go does the same before it probes a stream path, and skipping it here is
// why a usenet title was never seedable: its resolved path is "/webdav/<file>",
// no provider matched that, and the seed died as "automatic contribution source
// unavailable" while the very same file played fine.
//
// Usenet is the case worth getting right. It lands as a real local file, so it
// archives at disk speed through a file-backed grant, with no expiring address
// and no debrid API calls competing with playback.
func normalizeAutoSeedStreamPath(value string) string {
	path := strings.TrimSpace(value)
	if trimmed := strings.TrimPrefix(path, "/webdav/"); trimmed != path {
		return "/" + trimmed
	}
	if trimmed := strings.TrimPrefix(path, "webdav/"); trimmed != path {
		return "/" + trimmed
	}
	return path
}

// Both transports are now authenticated source grants; what differs is what is
// behind the grant. A path that resolves to a file this process already holds is
// published from that open file. A path that resolves to somebody else's CDN — a
// debrid or usenet stream — is published from a grant backed by the streaming
// layer, which re-resolves the expiring address underneath every range the
// relay asks for. No media bytes are buffered in this process either way, and
// no address ever leaves it.
//
// The resolution still happens here and never on the player's request path. It
// is no longer done to obtain something to hand over — it is done to learn which
// kind of source this is, and it warms the streaming layer's address cache for
// the first range the relay asks for.
//
// The stream path is normalized first: see normalizeAutoSeedStreamPath.
func (h *PearTubeHandler) planQualifiedAutoSeed(ctx context.Context, relay *peartube.Client, req SeedRequest) (func(context.Context) (*peartube.ArchiveJob, error), error) {
	streamPath := normalizeAutoSeedStreamPath(req.StreamPath)
	if h.streams == nil || streamPath == "" {
		return nil, errAutoSeedSourceUnavailable
	}
	resolved, err := h.streams.GetDirectURL(ctx, streamPath)
	if err != nil {
		return nil, errAutoSeedSourceUnavailable
	}
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return nil, errAutoSeedSourceUnavailable
	}
	req.StreamPath = ""
	req.retentionClass = peartube.RetentionClassContributionCache
	if strings.HasPrefix(resolved, "http://") || strings.HasPrefix(resolved, "https://") {
		req.FilePath = ""
		req.SourceURL = ""
		return h.planRemoteAutoSeed(relay, req, streamPath)
	}
	req.FilePath = resolved
	req.SourceURL = ""
	return h.planSeed(ctx, relay, req)
}

// planRemoteAutoSeed grants the relay range access to a remote title instead of
// handing it an address.
//
// The address is what used to make this fragile: it expires in about ten
// minutes, so an archive that took forty-two minutes lost every byte it had
// transferred the moment the viewer moved on. A grant has none of that coupling.
// It is bound to the job, not to a session; the streaming layer re-resolves
// underneath it; and it is revoked only when the job reaches a terminal status
// or consent is withdrawn.
//
// The consent re-check is the same one the local path makes, and for the same
// reason: settings can change between the heartbeat that decided to seed and the
// moment the request is actually sent, and a withdrawn consent must stop it. It
// now also pins the source-grant policy epoch, so a consent downgrade that races
// grant preparation cannot be followed by a live capability.
func (h *PearTubeHandler) planRemoteAutoSeed(relay *peartube.Client, req SeedRequest, streamPath string) (func(context.Context) (*peartube.ArchiveJob, error), error) {
	coordinates := seedCoordinates(req)
	if err := coordinates.Validate(); err != nil {
		return nil, err
	}
	// The grant is served by the same provider playback streams through, which
	// is what makes debrid re-resolution free here. A resolver that cannot serve
	// ranges is not a source this process can grant access to.
	reader, rangeReadable := h.streams.(peartube.RemoteRangeReader)
	if !rangeReadable {
		return nil, errAutoSeedSourceUnavailable
	}
	archive := peartube.ArchiveRemoteRequest{
		Source: peartube.RemoteSource{Reader: reader, StreamPath: streamPath},
		// Watched media is a contribution, not a deliberate archive pin: it is
		// charged against the contribution budget the viewer consented to and
		// evicted by that budget's rules.
		RetentionClass:     peartube.RetentionClassContributionCache,
		ArchiveCoordinates: coordinates,
	}
	archive.SourceGrantPolicyEpoch = h.sourceGrants.PolicyEpoch()
	// The stream path is the stable identity: the same episode resolved twice is
	// the same seed. Keying on an address would submit it again on every play.
	archive.IdempotencyKey = seedIdempotencyKey(coordinates, "stream:"+streamPath)
	if err := archive.Validate(); err != nil {
		return nil, err
	}
	log.Printf("[peartube] seeding %s tmdb=%s title=%q from an authenticated remote source grant",
		coordinates.ContentKind, coordinates.TMDBID, coordinates.TMDBTitle)
	return func(ctx context.Context) (*peartube.ArchiveJob, error) {
		h.retentionMu.RLock()
		defer h.retentionMu.RUnlock()
		retentionAllowed := func() bool {
			h.configMu.RLock()
			currentRelay, currentPolicy := h.relay, h.resolved
			h.configMu.RUnlock()
			return currentRelay == relay &&
				!currentPolicy.MigrationRequired &&
				currentPolicy.ConsentVersion == config.PearTubeConsentVersion &&
				currentPolicy.ContributeWatchedMedia &&
				h.sourceGrants.PolicyEpoch() == archive.SourceGrantPolicyEpoch
		}
		if !retentionAllowed() {
			return nil, errors.New("explicit retention consent is required")
		}
		if err := h.reconcileRelayPolicy(ctx, relay); err != nil {
			return nil, err
		}
		if !retentionAllowed() {
			return nil, errors.New("explicit retention consent is required")
		}
		return relay.ArchiveRemoteSource(ctx, archive, h.sourceGrants)
	}, nil
}

// planSeed picks the relay transport a seed request needs and validates
// everything that can be checked without a round trip. Only the returned
// closure talks to the relay, so a caller mistake stays a client error.
func (h *PearTubeHandler) planSeed(ctx context.Context, relay *peartube.Client, req SeedRequest) (func(context.Context) (*peartube.ArchiveJob, error), error) {
	coordinates := seedCoordinates(req)

	onDisk := strings.TrimSpace(req.LocalMediaItemID) != "" || strings.TrimSpace(req.FilePath) != ""
	remote := strings.TrimSpace(req.SourceURL) != "" || strings.TrimSpace(req.StreamPath) != ""

	switch {
	case onDisk && remote:
		return nil, errors.New("seed either a local library item or a remote source, not both")

	case remote:
		return nil, errors.New("remote and debrid sources cannot be archived; use an authenticated local source")

	case onDisk:
		archive, err := h.buildArchiveRequest(ctx, req, coordinates)
		if err != nil {
			return nil, err
		}
		archive.SourceGrantPolicyEpoch = h.sourceGrants.PolicyEpoch()
		sourceIdentity := "file:" + archive.FilePath
		if itemID := strings.TrimSpace(req.LocalMediaItemID); itemID != "" {
			sourceIdentity = "local:" + itemID
		}
		archive.IdempotencyKey = seedIdempotencyKey(archive.ArchiveCoordinates, sourceIdentity)
		log.Printf("[peartube] seeding %s tmdb=%s title=%q from authenticated local source",
			archive.ContentKind, archive.TMDBID, archive.TMDBTitle)
		return func(ctx context.Context) (*peartube.ArchiveJob, error) {
			h.retentionMu.RLock()
			defer h.retentionMu.RUnlock()
			retentionAllowed := func() bool {
				h.configMu.RLock()
				currentRelay, currentPolicy := h.relay, h.resolved
				h.configMu.RUnlock()
				if currentRelay != relay ||
					currentPolicy.MigrationRequired ||
					currentPolicy.ConsentVersion != config.PearTubeConsentVersion ||
					h.sourceGrants.PolicyEpoch() != archive.SourceGrantPolicyEpoch {
					return false
				}
				if archive.RetentionClass == peartube.RetentionClassContributionCache {
					return currentPolicy.ContributeWatchedMedia
				}
				return currentPolicy.ArchiveEnabled
			}
			if !retentionAllowed() {
				return nil, errors.New("explicit retention consent is required")
			}
			if err := h.reconcileRelayPolicy(ctx, relay); err != nil {
				return nil, err
			}
			if !retentionAllowed() {
				return nil, errors.New("explicit retention consent is required")
			}
			return relay.ArchiveSource(ctx, archive, h.sourceGrants)
		}, nil

	default:
		return nil, errors.New("localMediaItemId or filePath is required")
	}
}

// SeedStatus proxies the relay's job status so the frontend polls one origin.
func (h *PearTubeHandler) SeedStatus(w http.ResponseWriter, r *http.Request) {
	relay := h.currentRelay()
	if relay == nil {
		writeJSONError(w, "peartube relay is not configured", http.StatusServiceUnavailable)
		return
	}
	status, err := relay.ArchiveStatus(r.Context(), mux.Vars(r)["jobId"])
	if err != nil {
		var apiErr *peartube.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			writeJSONError(w, "seed job not found", http.StatusNotFound)
			return
		}
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}
	if status.Status == "completed" || status.Status == "failed" || status.Status == "cancelled" {
		h.sourceGrants.RevokeJob(status.JobID)
	}
	writeJSON(w, http.StatusOK, status)
}

// buildArchiveRequest resolves the local-library form of a seed request to a
// file this process may publish.
func (h *PearTubeHandler) buildArchiveRequest(ctx context.Context, req SeedRequest, coordinates peartube.ArchiveCoordinates) (peartube.ArchiveRequest, error) {
	archive := peartube.ArchiveRequest{
		ArchiveCoordinates: coordinates,
		RetentionClass:     req.retentionClass,
	}

	itemID := strings.TrimSpace(req.LocalMediaItemID)
	if itemID != "" {
		if h.localMedia == nil {
			return archive, errors.New("local media service unavailable")
		}
		item, err := h.localMedia.GetItem(ctx, itemID)
		if err != nil {
			return archive, err
		}
		archive.FilePath = item.FilePath
		applyLocalMediaMetadata(&archive.ArchiveCoordinates, item)
	}

	if path := strings.TrimSpace(req.FilePath); path != "" {
		resolved, err := h.resolveLibraryPath(ctx, path)
		if err != nil {
			return archive, err
		}
		archive.FilePath = resolved
	}

	if archive.FilePath == "" {
		return archive, errors.New("localMediaItemId or filePath is required")
	}
	if err := archive.Validate(); err != nil {
		return archive, err
	}
	return archive, nil
}

// applyLocalMediaMetadata fills in the TMDB coordinates the caller did not
// supply from what the library scanner matched. It never overwrites an explicit
// value: the caller is the one looking at the detail screen.
func applyLocalMediaMetadata(archive *peartube.ArchiveCoordinates, item *models.LocalMediaItem) {
	if archive.TMDBID == "" && item.ExternalIDs != nil {
		archive.TMDBID = strings.TrimSpace(item.ExternalIDs.TMDB)
	}
	if archive.TMDBID == "" {
		// A matched item's title id is its TMDB id; anything else is not a
		// coordinate the relay accepts.
		if id := strings.TrimSpace(item.MatchedTitleID); isTMDBID(id) {
			archive.TMDBID = id
		}
	}
	if archive.TMDBTitle == "" {
		archive.TMDBTitle = strings.TrimSpace(item.MatchedName)
	}
	if archive.TMDBTitle == "" {
		archive.TMDBTitle = strings.TrimSpace(item.DetectedTitle)
	}
	if archive.TMDBYear == 0 {
		archive.TMDBYear = item.MatchedYear
	}
	if archive.TMDBYear == 0 {
		archive.TMDBYear = item.DetectedYear
	}
	if archive.ContentKind == "" {
		if item.SeasonNumber > 0 && item.EpisodeNumber > 0 {
			archive.ContentKind = "episode"
		} else if item.LibraryType == models.LocalMediaLibraryTypeMovie {
			archive.ContentKind = "movie"
		}
	}
	if archive.ContentKind == "episode" {
		if archive.TMDBSeason == 0 {
			archive.TMDBSeason = item.SeasonNumber
		}
		if archive.TMDBEpisode == 0 {
			archive.TMDBEpisode = item.EpisodeNumber
		}
	}
	if archive.Overview == "" {
		archive.Overview = strings.TrimSpace(item.EpisodeOverview)
	}
}

// resolveLibraryPath confirms an explicit path names a real file inside a
// configured library root. Without this, an authenticated account could publish
// any file this process can read into a public swarm.
func (h *PearTubeHandler) resolveLibraryPath(ctx context.Context, path string) (string, error) {
	if h.localMedia == nil {
		return "", errors.New("local media service unavailable")
	}
	resolved, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", errors.New("resolve local media source")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", errors.New("stat local media source")
	}
	if info.IsDir() {
		return "", errors.New("filePath is a directory")
	}
	libraries, err := h.localMedia.ListLibraries(ctx)
	if err != nil {
		return "", err
	}
	for _, library := range libraries {
		root, err := filepath.Abs(filepath.Clean(library.RootPath))
		if err != nil || root == "" {
			continue
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(root, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", errors.New("filePath is not inside a configured local media library")
}

func isTMDBID(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
