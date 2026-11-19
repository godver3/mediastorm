package handlers

import (
	"log"
	"strings"
	"sync"
	"time"

	"novastream/models"
	"novastream/services/badstreams"
)

// streamReleaseTTL bounds how long a resolved path stays attributable to its
// release; a movie or a long episode is well inside it.
const streamReleaseTTL = 12 * time.Hour

// streamReleaseRegistry remembers which release each resolved stream path came
// from, so a content failure found mid-playback (missing Usenet articles, a
// corrupted file) marks that release bad and later searches stop offering it.
// Before this only failures found while resolving were marked, and a release
// that broke during playback was picked again on the next Play.
type streamReleaseRegistry struct {
	mu         sync.Mutex
	releases   map[string]streamRelease
	badStreams *badstreams.Service
}

type streamRelease struct {
	request    badstreams.MarkRequest
	resolvedAt time.Time
}

var defaultStreamReleaseRegistry = &streamReleaseRegistry{
	releases: make(map[string]streamRelease),
}

// SetStreamReleaseBadStreams wires the registry that confirmed mid-playback
// content failures are recorded in.
func SetStreamReleaseBadStreams(service *badstreams.Service) {
	defaultStreamReleaseRegistry.mu.Lock()
	defaultStreamReleaseRegistry.badStreams = service
	defaultStreamReleaseRegistry.mu.Unlock()
}

// rememberStreamRelease records the release a resolve produced `path` from.
func rememberStreamRelease(path string, result models.NZBResult) {
	defaultStreamReleaseRegistry.remember(path, result)
}

func (r *streamReleaseRegistry) remember(path string, result models.NZBResult) {
	key := normalizeStreamFailurePath(path)
	title := strings.TrimSpace(result.Title)
	if r == nil || key == "" || title == "" {
		return
	}
	provider := ""
	for _, attr := range []string{"provider", "debridProvider", "resolver"} {
		if provider = strings.TrimSpace(result.Attributes[attr]); provider != "" {
			break
		}
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releases[key] = streamRelease{
		request: badstreams.MarkRequest{
			ReleaseName: title,
			ServiceType: string(result.ServiceType),
			Provider:    provider,
			SourcePath:  path,
		},
		resolvedAt: now,
	}
	for k, release := range r.releases {
		if now.Sub(release.resolvedAt) > streamReleaseTTL {
			delete(r.releases, k)
		}
	}
}

// markBad marks the release behind `path` bad for a content failure. Transient
// trouble (a provider outage, a stream the server no longer has open) is left
// alone so a good release isn't blacklisted for it.
func (r *streamReleaseRegistry) markBad(path string, record streamFailureRecord) bool {
	if r == nil || !isContentStreamFailure(record.Reason) {
		return false
	}
	r.mu.Lock()
	release, ok := r.releases[normalizeStreamFailurePath(path)]
	service := r.badStreams
	r.mu.Unlock()
	if !ok || service == nil {
		return false
	}
	req := release.request
	req.Reason = "playback:" + record.Reason
	if _, err := service.Mark(req); err != nil {
		log.Printf("[stream-migration] failed to mark release bad after %s: release=%q err=%v", record.Reason, req.ReleaseName, err)
		return false
	}
	log.Printf("[stream-migration] marked release bad after %s during playback: release=%q service=%q provider=%q",
		record.Reason, req.ReleaseName, req.ServiceType, req.Provider)
	return true
}

func isContentStreamFailure(reason string) bool {
	switch reason {
	case "article_not_found", "partial_content_missing_articles", "corrupted_missing_articles", zeroFilledFailureReason, "provider_blocked_content":
		return true
	}
	return false
}

// reportConfirmedStreamFailure signals the player to migrate off `path` and,
// for a content failure, marks its release bad.
func reportConfirmedStreamFailure(path string, record streamFailureRecord) {
	GetStreamTracker().MarkPlaybackMigrationForPath(path, streamFailureMigrationReason(record))
	defaultStreamReleaseRegistry.markBad(path, record)
}
