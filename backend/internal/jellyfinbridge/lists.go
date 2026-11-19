package jellyfinbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// The profile's custom lists are Jellyfin collections (BoxSets, see
// listFolders) and, for the clients' "Add to playlist" dialogs, playlists:
// both APIs create lists and add or remove movies and series. A list holds
// titles, so an episode or season adds its series.

// listEditor is the part of the custom list service that changes lists. It
// is optional: without it the bridge's lists stay read-only.
type listEditor interface {
	CreateList(userID, name string) (models.CustomList, error)
	DeleteList(userID, listID string) (bool, error)
	AddItem(userID, listID string, input models.WatchlistUpsert) (models.WatchlistItem, error)
	RemoveItem(userID, listID, mediaType, id string) (bool, error)
}

// kindPlaylist is the Jellyfin item type the lists take in playlist queries.
const kindPlaylist = "Playlist"

// listsEditable reports whether lists can be changed through the bridge.
func (s *Server) listsEditable() bool {
	_, ok := s.listEditor()
	return ok
}

// listError answers a list service error: a list or entry gone elsewhere is
// a 404, anything else a 500.
func listError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "List not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func (s *Server) listEditor() (listEditor, bool) {
	if s.deps.CustomLists == nil {
		return nil, false
	}
	ed, ok := s.deps.CustomLists.(listEditor)
	return ed, ok
}

// ownList resolves a list item ID to the profile's list ID.
func (s *Server) ownList(p *principal, id string) (string, bool) {
	ref, ok := s.ids.get(id)
	if !ok || ref.Kind != refList || ref.Ctx.ProfileID != p.Profile.ID || ref.Ctx.Extra == "" {
		return "", false
	}
	return ref.Ctx.Extra, true
}

// listTitleFor is the title a list keeps for an item: movies and series as
// they are, episodes and seasons as their series.
func (s *Server) listTitleFor(r *http.Request, p *principal, id string) (titleInfo, bool) {
	ref, ok := s.ids.get(id)
	if !ok || !s.kidsAllowsRef(r.Context(), p.Profile, ref) {
		return titleInfo{}, false
	}
	switch ref.Kind {
	case refMovie, refSeries:
		return titleFromRef(ref), true
	case refSeason, refEpisode:
		t := seriesInfoFromSeasonRef(ref)
		t.MediaType = "series"
		return t, t.ID != "" || len(t.ExternalIDs) > 0
	}
	return titleInfo{}, false
}

// listUpsert builds the list entry with metadata-derived identity, like the
// watchlist (favorites) does.
func (s *Server) listUpsert(ctx context.Context, t titleInfo) models.WatchlistUpsert {
	trusted := s.trustedTitle(ctx, t.MediaType, t.source(), t)
	return models.WatchlistUpsert{
		ID: t.source(), MediaType: t.MediaType, Name: trusted.Name, Year: trusted.Year,
		PosterURL: firstNonEmpty(trusted.Poster, t.Poster), BackdropURL: firstNonEmpty(trusted.Backdrop, t.Backdrop),
		ExternalIDs: sourceExternalIDs(trusted),
	}
}

// addToList adds the items it can and reports whether any was added.
func (s *Server) addToList(r *http.Request, p *principal, ed listEditor, listID string, ids []string) (int, error) {
	added := 0
	for _, id := range ids {
		t, ok := s.listTitleFor(r, p, id)
		if !ok {
			continue
		}
		if _, err := ed.AddItem(p.Profile.ID, listID, s.listUpsert(r.Context(), t)); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

func (s *Server) listsChanged(p *principal) {
	s.views.invalidate(p.Profile.ID)
	s.invalidateUserState(p.Profile.ID)
}

// createListRequest is CreatePlaylistDto; collections pass the same fields
// in the query string, and older clients do so for playlists too.
type createListRequest struct {
	Name string   `json:"Name"`
	Ids  []string `json:"Ids"`
}

func (s *Server) handleCreateList(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	ed, ok := s.listEditor()
	if !ok {
		writeError(w, http.StatusForbidden, "Lists cannot be changed")
		return
	}
	var in createListRequest
	if r.Body != nil && r.ContentLength != 0 {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in)
	}
	if v := queryValue(r, "Name"); v != "" {
		in.Name = v
	}
	in.Ids = append(in.Ids, queryList(r, "Ids")...)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}
	list, err := ed.CreateList(p.Profile.ID, name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.addToList(r, p, ed, list.ID, in.Ids); err != nil {
		// Not half-created: a retry would leave a duplicate list.
		_, _ = ed.DeleteList(p.Profile.ID, list.ID)
		listError(w, err)
		return
	}
	s.listsChanged(p)
	writeJSON(w, http.StatusOK, map[string]string{"Id": s.listItemID(p, list)})
}

// listItemID is the Jellyfin ID of a list (the same as listFolders gives).
func (s *Server) listItemID(p *principal, list models.CustomList) string {
	return s.ids.putTrusted(refList, p.Profile.ID+":"+list.ID, refContext{Name: list.Name, ProfileID: p.Profile.ID, Extra: list.ID})
}

func (s *Server) handleListAdd(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	ed, ok := s.listEditor()
	listID, own := s.ownList(p, pathVar(r, "id"))
	if !ok || !own {
		writeError(w, http.StatusNotFound, "List not found")
		return
	}
	if _, err := s.addToList(r, p, ed, listID, queryList(r, "Ids")); err != nil {
		listError(w, err)
		return
	}
	s.listsChanged(p)
	w.WriteHeader(http.StatusNoContent)
}

// handleListRemove removes items: collections name them by Ids, playlists
// by EntryIds (the PlaylistItemId, which is the item ID here).
func (s *Server) handleListRemove(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	ed, ok := s.listEditor()
	listID, own := s.ownList(p, pathVar(r, "id"))
	if !ok || !own {
		writeError(w, http.StatusNotFound, "List not found")
		return
	}
	for _, id := range append(queryList(r, "Ids"), queryList(r, "EntryIds")...) {
		t, ok := s.listTitleFor(r, p, id)
		if !ok {
			continue
		}
		if _, err := ed.RemoveItem(p.Profile.ID, listID, t.MediaType, t.source()); err != nil {
			listError(w, err)
			return
		}
	}
	s.listsChanged(p)
	w.WriteHeader(http.StatusNoContent)
}

// handlePlaylistItems lists a playlist's entries; each entry's
// PlaylistItemId is its item ID (a list holds a title once).
func (s *Server) handlePlaylistItems(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	listID, own := s.ownList(p, pathVar(r, "id"))
	if !own {
		writeError(w, http.StatusNotFound, "Playlist not found")
		return
	}
	u := s.newUserState(p.Profile.ID)
	titles := s.listTitles(p.Profile, listID)
	items := make([]dto.BaseItemDto, 0, len(titles))
	for _, t := range titles {
		item := s.titleItem(u, t)
		item.PlaylistItemID = item.ID
		items = append(items, item)
	}
	q := parseItemQuery(r)
	page := s.page(items, q)
	withPlayableSources(page.Items)
	writeJSON(w, http.StatusOK, page)
}

// handlePlaylist answers GET /Playlists/{id} (10.9+ clients read it before
// editing): a profile's list is private to it.
func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	listID, own := s.ownList(p, pathVar(r, "id"))
	if !own {
		writeError(w, http.StatusNotFound, "Playlist not found")
		return
	}
	ids := []string{}
	u := s.newUserState(p.Profile.ID)
	for _, t := range s.listTitles(p.Profile, listID) {
		ids = append(ids, s.titleItem(u, t).ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"OpenAccess": false, "Shares": []any{}, "ItemIds": ids})
}

// handleDeleteItem deletes a list or a recording; other items cannot be
// deleted through the bridge.
func (s *Server) handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if ref, ok := s.ids.get(pathVar(r, "id")); ok && ref.Kind == refRecording && ref.Ctx.ProfileID == p.Profile.ID {
		s.deleteRecording(w, r, p, ref.Source)
		return
	}
	ed, ok := s.listEditor()
	listID, own := s.ownList(p, pathVar(r, "id"))
	if !ok || !own {
		writeError(w, http.StatusForbidden, "Only lists and recordings can be deleted")
		return
	}
	if _, err := ed.DeleteList(p.Profile.ID, listID); err != nil {
		listError(w, err)
		return
	}
	s.listsChanged(p)
	w.WriteHeader(http.StatusNoContent)
}

// playlistFolders are the list folders as playlists.
func (s *Server) playlistFolders(profile models.User) []dto.BaseItemDto {
	_, editable := s.listEditor()
	out := s.listFolders(profile)
	for i := range out {
		out[i].Type = kindPlaylist
		out[i].MediaType = dto.MediaTypeVideo
		out[i].CanDelete = boolPtr(editable)
	}
	return out
}

// handlePlaylistUser answers GET /Playlists/{id}/Users/{userId}: the owner
// may edit its list. jellyfin-web asks this before it offers a playlist in
// "Add to playlist" or lets entries be removed.
func (s *Server) handlePlaylistUser(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if _, own := s.ownList(p, pathVar(r, "id")); !own || normalizeID(pathVar(r, "uid")) != s.userID(p.Profile) {
		writeError(w, http.StatusNotFound, "Playlist not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"UserId": s.userID(p.Profile), "CanEdit": s.listsEditable()})
}

// handlePlaylistUsers answers GET /Playlists/{id}/Users: a list is shared
// with nobody; its owner's own entry is all there is.
func (s *Server) handlePlaylistUsers(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if _, own := s.ownList(p, pathVar(r, "id")); !own {
		writeError(w, http.StatusNotFound, "Playlist not found")
		return
	}
	writeJSON(w, http.StatusOK, []map[string]any{{"UserId": s.userID(p.Profile), "CanEdit": s.listsEditable()}})
}
