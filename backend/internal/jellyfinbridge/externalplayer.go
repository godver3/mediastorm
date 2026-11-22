package jellyfinbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"novastream/internal/jellyfinbridge/dto"
)

// External players: Jellyfin's Android TV app can hand videos to an external
// player app. It then skips PlaybackInfo: it picks the media source whose ID
// is the item ID from the item itself and opens /Videos/{id}/stream directly.
// Items therefore always list that source, and a stream request without a
// play session opens one the way PlaybackInfo would.

const externalStreamWait = 90 * time.Second

// withPlayableSources gives movies and episodes without media sources the
// item's own source, so clients that read sources from items (external
// players) find something to play.
func withPlayableSources(items []dto.BaseItemDto) {
	for i := range items {
		item := &items[i]
		if item.MediaSources != nil || (item.Type != dto.KindMovie && item.Type != dto.KindEpisode && item.Type != dto.KindVideo) {
			continue
		}
		item.MediaSources = []dto.MediaSourceInfo{versionStub(item.ID, "mediastorm", item.RunTimeTicks)}
	}
}

// bufferWriter collects an in-process handler's response.
type bufferWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferWriter) Header() http.Header         { return b.header }
func (b *bufferWriter) WriteHeader(code int)        { b.status = code }
func (b *bufferWriter) Write(p []byte) (int, error) { return b.body.Write(p) }

// openStreamSession resolves a stream for a direct stream request that came
// without PlaybackInfo, by running PlaybackInfo in-process for the caller.
func (s *Server) openStreamSession(r *http.Request, p *principal, itemID, mediaSourceID string) (*playSession, bool) {
	ref, ok := s.ids.get(itemID)
	if !ok || (!playableVOD(ref.Kind) && ref.Kind != refRecording) {
		return nil, false
	}
	body, _ := json.Marshal(map[string]string{"MediaSourceId": strings.TrimSpace(mediaSourceID)})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "/items/"+itemID+"/playbackinfo", bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header = r.Header.Clone()
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	req = mux.SetURLVars(withPrincipal(req, p), map[string]string{"id": itemID})
	out := &bufferWriter{header: http.Header{}, status: http.StatusOK}
	s.handlePlaybackInfo(out, req)
	var info dto.PlaybackInfoResponse
	if out.status != http.StatusOK || json.Unmarshal(out.body.Bytes(), &info) != nil || info.PlaySessionID == "" {
		return nil, false
	}
	sess, ok := s.plays.get(info.PlaySessionID)
	if !ok {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), externalStreamWait)
	defer cancel()
	s.awaitPrequeue(ctx, p, sess, externalStreamWait)
	return sess, true
}
