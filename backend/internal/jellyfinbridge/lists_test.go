package jellyfinbridge

import (
	"net/http"
	"net/url"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/services/customlists"
)

func listHarness(t *testing.T) (*harness, *customlists.Service) {
	t.Helper()
	h := newHarness(t, config.JellyfinBridgeSettings{})
	lists, err := customlists.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.deps.CustomLists = lists
	return h, lists
}

// A list created from a client's "Add to collection" dialog is a mediastorm
// custom list; the playlist dialog sees and edits the same lists.
func TestListsEditableFromClients(t *testing.T) {
	h, lists := listHarness(t)
	tok := h.login("Living Room", "alice-pass")
	movie := movieID(t, h, tok)

	rec := h.do(http.MethodPost, "/Collections?Name="+url.QueryEscape("Movie night")+"&Ids="+movie, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	created := decode[map[string]string](t, rec)
	listID := created["Id"]
	own, _ := lists.ListLists(h.alice.ID)
	if len(own) != 1 || own[0].Name != "Movie night" {
		t.Fatalf("lists = %+v", own)
	}
	if items, _ := lists.ListItems(h.alice.ID, own[0].ID); len(items) != 1 || items[0].MediaType != "movie" {
		t.Fatalf("list items = %+v", items)
	}

	// The playlist dialog lists it, deletable.
	pls := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Items?Recursive=true&IncludeItemTypes=Playlist", tok, nil))
	if len(pls.Items) != 1 || pls.Items[0].ID != listID || pls.Items[0].Type != "Playlist" || pls.Items[0].CanDelete == nil || !*pls.Items[0].CanDelete {
		t.Fatalf("playlists = %+v", pls.Items)
	}

	// An episode adds its series.
	next := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Shows/NextUp", tok, nil))
	if len(next.Items) == 0 {
		t.Fatal("no next-up episode")
	}
	if rec := h.do(http.MethodPost, "/Playlists/"+listID+"/Items?Ids="+next.Items[0].ID, tok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	entries := decode[dto.QueryResult[dto.BaseItemDto]](t, h.do(http.MethodGet, "/Playlists/"+listID+"/Items", tok, nil))
	if len(entries.Items) != 2 || entries.Items[0].PlaylistItemID == "" {
		t.Fatalf("entries = %+v", entries.Items)
	}
	hasSeries := false
	for _, e := range entries.Items {
		hasSeries = hasSeries || e.Type == dto.KindSeries
	}
	if !hasSeries {
		t.Fatalf("episode did not add its series: %+v", entries.Items)
	}

	// Another account can neither change nor delete it.
	bob := h.login("Living Room", "bob-pass")
	if rec := h.do(http.MethodPost, "/Collections/"+listID+"/Items?Ids="+movie, bob, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bob add: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/Items/"+listID, bob, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("bob delete: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/Items/"+movie, tok, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("deleting a movie: %d", rec.Code)
	}

	// Removing by playlist entry, then deleting the list.
	if rec := h.do(http.MethodDelete, "/Playlists/"+listID+"/Items?EntryIds="+movie, tok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d", rec.Code)
	}
	if items, _ := lists.ListItems(h.alice.ID, own[0].ID); len(items) != 1 || items[0].MediaType != "series" {
		t.Fatalf("after remove = %+v", items)
	}
	if rec := h.do(http.MethodDelete, "/Items/"+listID, tok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if left, _ := lists.ListLists(h.alice.ID); len(left) != 0 {
		t.Fatalf("lists after delete = %+v", left)
	}
}

// 10.9+ clients create playlists with a JSON body.
func TestCreatePlaylistWithBody(t *testing.T) {
	h, lists := listHarness(t)
	tok := h.login("Living Room", "alice-pass")
	movie := movieID(t, h, tok)
	rec := h.do(http.MethodPost, "/Playlists", tok, map[string]any{"Name": "Later", "Ids": []string{movie}, "MediaType": "Video"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	own, _ := lists.ListLists(h.alice.ID)
	if len(own) != 1 || own[0].Name != "Later" {
		t.Fatalf("lists = %+v", own)
	}
	info := decode[map[string]any](t, h.do(http.MethodGet, "/Playlists/"+decode[map[string]string](t, rec)["Id"], tok, nil))
	if ids, _ := info["ItemIds"].([]any); len(ids) != 1 {
		t.Fatalf("playlist info = %+v", info)
	}
	if rec := h.do(http.MethodPost, "/Playlists", tok, map[string]any{"Name": " "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty name: %d", rec.Code)
	}
}

// jellyfin-web offers an existing playlist, and "Add to collection", only
// when the server says the user may edit them.
func TestListPermissionsForJellyfinWeb(t *testing.T) {
	h, _ := listHarness(t)
	tok := h.login("Living Room", "alice-pass")
	me := decode[dto.UserDto](t, h.do(http.MethodGet, "/Users/Me", tok, nil))
	if me.Policy == nil || !me.Policy.EnableCollectionManagement {
		t.Fatal("collection management not enabled")
	}
	created := decode[map[string]string](t, h.do(http.MethodPost, "/Playlists", tok, map[string]any{"Name": "Mine"}))
	perm := decode[map[string]any](t, h.do(http.MethodGet, "/Playlists/"+created["Id"]+"/Users/"+me.ID, tok, nil))
	if perm["CanEdit"] != true {
		t.Fatalf("permissions = %+v", perm)
	}
	bob := h.login("Living Room", "bob-pass")
	bobMe := decode[dto.UserDto](t, h.do(http.MethodGet, "/Users/Me", bob, nil))
	if rec := h.do(http.MethodGet, "/Playlists/"+created["Id"]+"/Users/"+bobMe.ID, bob, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("another account's permission check: %d", rec.Code)
	}
}
