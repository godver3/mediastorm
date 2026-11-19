package handlers

import (
	"errors"
	"log"
	"net/url"
	"path"
	"strings"

	"novastream/internal/requestsecurity"
	"novastream/services/badstreams"
)

// zeroFillRunThreshold is the longest run of zero bytes a real media file is
// expected to hold. External Usenet engines (AltMount and its peers) keep a
// stream going past an article missing on every provider by serving the whole
// article as zeros — 384–768 KiB per hole — and only note it in their own
// health table. Compressed audio/video never contains a run anywhere near this
// long, so a proxied run at least this long is the engine's padding: the file
// is damaged at that point and players such as ExoPlayer fail on it.
const zeroFillRunThreshold = 256 << 10

// errExternalUsenetZeroFilled classifies a stream whose engine served padding
// for missing articles, so the shared failure path treats it as content damage.
var errExternalUsenetZeroFilled = errors.New("external usenet engine zero-filled missing articles")

const zeroFilledFailureReason = "zero_filled_missing_articles"

// zeroRunDetector tracks the current run of zero bytes across the chunks of
// one proxied response and fires once when it reaches zeroFillRunThreshold.
type zeroRunDetector struct {
	run     int64
	tripped bool
}

// observe consumes the next chunk and reports true exactly once, on the chunk
// that completes the first qualifying run.
func (d *zeroRunDetector) observe(p []byte) bool {
	if d == nil || d.tripped {
		return false
	}
	for _, b := range p {
		if b != 0 {
			d.run = 0
			continue
		}
		d.run++
		if d.run >= zeroFillRunThreshold {
			d.tripped = true
			return true
		}
	}
	return false
}

// newExternalUsenetZeroRunDetector returns a detector when streamURL is served
// by an enabled external Usenet engine, and nil otherwise (debrid CDNs, addon
// proxies and other external URLs are not engine-padded).
func (h *VideoHandler) newExternalUsenetZeroRunDetector(streamURL *url.URL) *zeroRunDetector {
	if h == nil || h.configManager == nil {
		return nil
	}
	if _, ok := externalUsenetEngineFor(h.configManager, streamURL); !ok {
		return nil
	}
	return &zeroRunDetector{}
}

// reportZeroFilledExternalStream handles an engine-padded stream: it signals
// the player to migrate, marks the release bad so later searches and prewarms
// stop picking it, and drops ready prequeues that point at the damaged file.
// Playback of the current response continues; some players skip the damage.
func (h *VideoHandler) reportZeroFilledExternalStream(streamPath string, offset int64) {
	if record, recent := h.failures.confirmedRecent(streamPath, streamFailureConfirmationTTL); recent && record.Reason == zeroFilledFailureReason {
		return
	}
	log.Printf("[stream-migration] external Usenet engine served zero-filled data for missing articles: path=%q offset=%d",
		requestsecurity.URLForLog(streamPath), offset)
	record, confirmed := h.failures.recordRecognizedFailure(streamPath, errExternalUsenetZeroFilled)
	if !confirmed {
		return
	}
	GetStreamTracker().MarkPlaybackMigrationForPath(streamPath, streamFailureMigrationReason(record))
	if !defaultStreamReleaseRegistry.markBad(streamPath, record) {
		category := ""
		if parsed, err := url.Parse(streamPath); err == nil {
			if engine, ok := externalUsenetEngineFor(h.configManager, parsed); ok {
				category = engine.Category
			}
		}
		markZeroFilledReleaseFromURL(streamPath, category, record)
	}
	h.invalidatePrequeuesForFailedPath(streamPath)
}

// markZeroFilledReleaseFromURL marks the release named by the engine's WebDAV
// folder when the resolve that produced the URL is no longer remembered (for
// example after a backend restart). Engines name the folder after the
// submitted NZB, which is the release title.
func markZeroFilledReleaseFromURL(streamPath, category string, record streamFailureRecord) {
	release := externalUsenetReleaseFromURL(streamPath, category)
	defaultStreamReleaseRegistry.mu.Lock()
	service := defaultStreamReleaseRegistry.badStreams
	defaultStreamReleaseRegistry.mu.Unlock()
	if release == "" || service == nil {
		return
	}
	req := badstreams.MarkRequest{
		ReleaseName: release,
		ServiceType: "usenet",
		SourcePath:  streamPath,
		Reason:      "playback:" + record.Reason,
	}
	if _, err := service.Mark(req); err != nil {
		log.Printf("[stream-migration] failed to mark release bad after %s: release=%q err=%v", record.Reason, release, err)
		return
	}
	log.Printf("[stream-migration] marked release bad after %s during playback: release=%q service=%q (from engine path)",
		record.Reason, release, req.ServiceType)
}

// externalUsenetReleaseFromURL returns the folder directly holding the file —
// or the file name itself when the file sits at the WebDAV root or directly in
// the engine's category folder — without a video extension (engines keep
// "Name.mkv" when the NZB was "Name.mkv.nzb").
func externalUsenetReleaseFromURL(streamPath, category string) string {
	parsed, err := url.Parse(strings.TrimSpace(streamPath))
	if err != nil {
		return ""
	}
	clean := path.Clean("/" + strings.Trim(parsed.Path, "/"))
	dir, file := path.Split(clean)
	release := path.Base(strings.TrimSuffix(dir, "/"))
	if release == "" || release == "/" || release == "." || release == "webdav" ||
		(strings.TrimSpace(category) != "" && strings.EqualFold(release, strings.TrimSpace(category))) {
		release = file
	}
	switch strings.ToLower(path.Ext(release)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".ts", ".m2ts", ".mov", ".webm":
		release = strings.TrimSuffix(release, path.Ext(release))
	}
	return strings.TrimSpace(release)
}
