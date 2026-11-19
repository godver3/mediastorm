package jellyfinbridge

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"novastream/services/playback"
)

// With "Disable Video Transcoding" a release whose video cannot be copied
// (codec, or a keyframe index too sparse to cut) does not play at all. The
// automatic version then moves on to the next ranked release, like the
// pick that avoids transcoding does when transcoding is allowed.

// errUncopyable marks a stream mediastorm refused because video transcoding
// is disabled and its video cannot be copied.
var errUncopyable = errors.New("video cannot be copied")

const (
	// maxUncopyableReleases bounds the releases skipped per item, so a title
	// none of whose releases copies stops resolving more of them.
	maxUncopyableReleases = 4
	uncopyableSwapBudget  = 90 * time.Second
)

// uncopyableReleases are the releases refused for a profile and item: their
// names, and their stream paths (a prequeue's own pick may carry no name).
type uncopyableReleases struct {
	names     []string
	paths     []string
	exhausted bool // a swap found no other release: stop looking
	at        time.Time
}

// uncopyableMu serialises the read-modify-write of entries (two devices on
// one item).
var uncopyableMu sync.Mutex

func uncopyableKey(profileID, itemID string) string { return profileID + "|" + itemID }

// uncopyableEntry returns what was refused for the item recently.
func (s *Server) uncopyableEntry(profileID, itemID string) uncopyableReleases {
	v, ok := s.uncopyable.Load(uncopyableKey(profileID, itemID))
	if !ok {
		return uncopyableReleases{}
	}
	entry := v.(uncopyableReleases)
	if time.Since(entry.at) > copyFailureTTL {
		s.uncopyable.Delete(uncopyableKey(profileID, itemID))
		return uncopyableReleases{}
	}
	return entry
}

// noteUncopyable records a refused release and returns the item's entry.
func (s *Server) noteUncopyable(profileID, itemID, name, streamPath string) uncopyableReleases {
	uncopyableMu.Lock()
	defer uncopyableMu.Unlock()
	entry := s.uncopyableEntry(profileID, itemID)
	if name != "" && !skipsRelease(entry.names, name) {
		entry.names = append(slices.Clip(entry.names), name)
	}
	if streamPath != "" && !slices.Contains(entry.paths, streamPath) {
		entry.paths = append(slices.Clip(entry.paths), streamPath)
	}
	entry.at = time.Now()
	s.uncopyable.Store(uncopyableKey(profileID, itemID), entry)
	return entry
}

func (s *Server) markUncopyableExhausted(profileID, itemID string) {
	uncopyableMu.Lock()
	defer uncopyableMu.Unlock()
	entry := s.uncopyableEntry(profileID, itemID)
	entry.exhausted, entry.at = true, time.Now()
	s.uncopyable.Store(uncopyableKey(profileID, itemID), entry)
}

// uncopyableReleaseName is the release a ready prequeue plays: the result it
// selected, else its display name.
func uncopyableReleaseName(st *playback.PrequeueStatusResponse) string {
	if st.SelectedResult != nil && strings.TrimSpace(st.SelectedResult.Title) != "" {
		return st.SelectedResult.Title
	}
	return firstNonEmpty(st.PassthroughName, st.DisplayName)
}

func skipsRelease(names []string, title string) bool {
	for _, name := range names {
		if sameRelease(title, &playback.PrequeueStatusResponse{DisplayName: name}) || strings.EqualFold(strings.TrimSpace(title), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// swapUncopyableRelease starts replacing the session's release after it was
// refused as uncopyable. It reports whether the session is (now) looking for
// another release; false leaves the failure to the caller.
func (s *Server) swapUncopyableRelease(p *principal, sess *playSession) bool {
	if sess.Live || sess.ReleaseKey != "" || s.deps.Handlers.IndexerSearch == nil || s.deps.Handlers.PlaybackResolve == nil {
		return false // a release the user picked stays that release
	}
	if sess.Ref.Kind != refMovie && sess.Ref.Kind != refEpisode {
		return false // no other releases (a recording, a library file)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.swapping {
		return true
	}
	failed := sess.status
	if failed == nil || failed.Status != playback.PrequeueStatusReady {
		return false
	}
	failedName := uncopyableReleaseName(failed)
	entry := s.noteUncopyable(sess.ProfileID, sess.ItemID, failedName, failed.StreamPath)
	if refused := max(len(entry.names), len(entry.paths)); entry.exhausted || refused > maxUncopyableReleases {
		log.Printf("[jellyfin-bridge] %s: %d releases cannot be copied and no other is left", sess.ItemID, refused)
		return false
	}
	skip := entry
	sess.swapping = true
	sess.swaps++
	// awaitPrequeue now waits for the swap instead of re-reading the prequeue.
	sess.prequeueID = ""
	sess.status = &playback.PrequeueStatusResponse{Status: playback.PrequeueStatusSearching}
	go s.runUncopyableSwap(p, sess, failedName, skip)
	return true
}

func (s *Server) runUncopyableSwap(p *principal, sess *playSession, failedName string, skip uncopyableReleases) {
	ctx, cancel := context.WithTimeout(context.Background(), uncopyableSwapBudget)
	defer cancel()
	titleID := ""
	if req, err := s.prequeueRequest(ctx, p, sess.Ref); err == nil {
		titleID = req.TitleID
	}
	var status *playback.PrequeueStatusResponse
	attempts := 0
	for _, r := range s.releasesWait(p, sess.ItemID, sess.Ref, autoReleaseWait) {
		if attempts == autoPickAttempts || ctx.Err() != nil {
			break
		}
		if skipsRelease(skip.names, r.Title) {
			continue
		}
		attempts++
		st, err := s.resolveReleaseStatus(ctx, p, sess, r, titleID)
		if err != nil {
			log.Printf("[jellyfin-bridge] uncopyable: release %q for %s: %v", r.Title, sess.ItemID, err)
			continue
		}
		if st.StreamPath != "" && slices.Contains(skip.paths, st.StreamPath) {
			continue // the refused release under another name
		}
		log.Printf("[jellyfin-bridge] %q cannot be copied (video transcoding disabled); playing %q for %s instead", failedName, r.Title, sess.ItemID)
		status = st
		break
	}
	if status == nil {
		log.Printf("[jellyfin-bridge] %s: no other release to play without video transcoding", sess.ItemID)
		s.markUncopyableExhausted(sess.ProfileID, sess.ItemID)
		status = &playback.PrequeueStatusResponse{Status: playback.PrequeueStatusFailed, Error: "no release can be played without video transcoding"}
	}
	sess.mu.Lock()
	sess.status = status
	if status.Duration > 0 {
		sess.duration = status.Duration
	}
	sess.swapping = false
	sess.mu.Unlock()
}
