package jellyfinbridge

import (
	"context"
	"log"
	"time"
)

// Stopping a playback from mediastorm's Active Streams: the dashboard marks
// the playback (profile, media type, item), and the bridge picks the mark up
// on the client's next progress report. It ends the play session, tells the
// device to stop over its socket (Jellyfin's Playstate command), and refuses
// further stream requests for the item, since native players such as mpv
// otherwise just open the next range request. Starting the item again
// (PlaybackInfo) lifts the block, but not right after the stop: Plezy and
// other clients treat the cut stream as dropped and reload it at once.

// stoppedPlaybackTTL bounds how long a stopped item stays blocked when the
// client never asks for it again.
const stoppedPlaybackTTL = 10 * time.Minute

// stopReloadGrace is how long after a stop a PlaybackInfo is taken for the
// client reloading the cut stream rather than the user starting it again.
const stopReloadGrace = 30 * time.Second

// stopPlayback ends a playback the server was asked to stop.
func (s *Server) stopPlayback(ctx context.Context, p *principal, itemID string, sess *playSession) {
	s.stopped.Store(chaptersKey(p.Profile.ID, itemID), time.Now())
	if sess == nil {
		sess, _ = s.plays.latestForItem(p.Profile.ID, itemID, p.Client.DeviceID, "")
	}
	if sess != nil {
		s.plays.remove(sess.ID)
		s.stopHLS(ctx, p, sess)
	}
	s.sockets.sendToDevice(p.Profile.ID, p.Client.DeviceID, p.Session.Token, "Playstate", map[string]any{"Command": "Stop"})
	log.Printf("[jellyfin-bridge] stopped playback of %s on %s (requested from Active Streams)", itemID, firstNonEmpty(p.Client.Client, p.Client.DeviceID, "a device"))
}

// playbackStopped reports whether an item was stopped and not started again.
func (s *Server) playbackStopped(profileID, itemID string) bool {
	v, ok := s.stopped.Load(chaptersKey(profileID, itemID))
	if !ok {
		return false
	}
	if time.Since(v.(time.Time)) > stoppedPlaybackTTL {
		s.stopped.Delete(chaptersKey(profileID, itemID))
		return false
	}
	return true
}

// clearStopped lifts the block when the profile starts the item again.
func (s *Server) clearStopped(profileID, itemID string) {
	key := chaptersKey(profileID, itemID)
	if v, ok := s.stopped.Load(key); ok && time.Since(v.(time.Time)) < stopReloadGrace {
		return // the client reloading the stream it was just cut off from
	}
	s.stopped.Delete(key)
}
