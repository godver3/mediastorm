package jellyfinbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// fakeRooms keeps watch rooms in memory with the real service's rules:
// the first play puts a lobby room in waiting-for-ready, all joined members
// ready releases it, playing rooms report their position at read time.
type fakeRooms struct {
	mu      sync.Mutex
	n       int
	rooms   map[string]*models.WatchRoom
	invites map[string][]string
	names   map[string]string
	created []models.WatchRoomCreate
	beats   int
	// requireMember makes updates fail for profiles that have not joined,
	// like the real service.
	requireMember bool
	// seen is when a profile last heartbeated or joined (presence).
	seen map[string]time.Time
}

func newFakeRooms() *fakeRooms {
	return &fakeRooms{rooms: map[string]*models.WatchRoom{}, invites: map[string][]string{}, names: map[string]string{}, seen: map[string]time.Time{}}
}

func (f *fakeRooms) add(creator string, room models.WatchRoom, invitees ...string) *models.WatchRoom {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	room.ID = fmt.Sprintf("aaaaaaaa-bbbb-cccc-dddd-%012d", f.n)
	room.CreatorProfileID = creator
	room.CreatorName = f.names[creator]
	if room.Status == "" {
		room.Status = models.WatchRoomStatusLobby
	}
	if room.Params == nil {
		room.Params = map[string]string{}
	}
	room.AnchorUpdatedAt = time.Now().UTC()
	room.Members = []models.WatchRoomMember{{ProfileID: creator, Name: f.names[creator], IsCreator: true, Joined: true}}
	f.rooms[room.ID] = &room
	f.invites[room.ID] = invitees
	return &room
}

func (f *fakeRooms) invited(roomID, profileID string) bool {
	room := f.rooms[roomID]
	if room == nil {
		return false
	}
	if room.CreatorProfileID == profileID {
		return true
	}
	for _, id := range f.invites[roomID] {
		if id == profileID {
			return true
		}
	}
	return false
}

func (f *fakeRooms) joined(room *models.WatchRoom, profileID string) bool {
	for _, m := range room.Members {
		if m.ProfileID == profileID && m.Joined {
			return true
		}
	}
	return false
}

func (f *fakeRooms) view(room *models.WatchRoom) *models.WatchRoom {
	out := *room
	out.Members = append([]models.WatchRoomMember(nil), room.Members...)
	for i := range out.Members {
		out.Members[i].Present = time.Since(f.seen[room.ID+"|"+out.Members[i].ProfileID]) <= 15*time.Second
	}
	if out.Status == models.WatchRoomStatusPlaying && !out.WaitingForReady {
		out.Position += time.Since(out.AnchorUpdatedAt).Seconds()
	}
	return &out
}

func (f *fakeRooms) Create(_ context.Context, _, creatorID string, in models.WatchRoomCreate) (*models.WatchRoom, error) {
	f.mu.Lock()
	f.created = append(f.created, in)
	f.mu.Unlock()
	room := f.add(creatorID, models.WatchRoom{Title: in.Title, MediaType: in.MediaType, ItemID: in.ItemID, Params: in.Params}, in.InviteeProfileIDs...)
	return f.Get(context.Background(), room.ID, creatorID)
}

func (f *fakeRooms) Invitations(_ context.Context, profileID string) ([]models.WatchRoom, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.WatchRoom
	for id, room := range f.rooms {
		if room.Status != models.WatchRoomStatusEnded && f.invited(id, profileID) {
			out = append(out, *f.view(room))
		}
	}
	return out, nil
}

var errFakeNotFound = errors.New("watch room not found")

func (f *fakeRooms) Get(_ context.Context, roomID, profileID string) (*models.WatchRoom, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.invited(roomID, profileID) {
		return nil, errFakeNotFound
	}
	return f.view(f.rooms[roomID]), nil
}

func (f *fakeRooms) Join(ctx context.Context, roomID, profileID, clientID string, _ models.WatchRoomClientCapabilities) (*models.WatchRoom, error) {
	f.mu.Lock()
	if !f.invited(roomID, profileID) {
		f.mu.Unlock()
		return nil, errFakeNotFound
	}
	room := f.rooms[roomID]
	found := false
	for i := range room.Members {
		if room.Members[i].ProfileID == profileID {
			room.Members[i].Joined, room.Members[i].ClientID, found = true, clientID, true
		}
	}
	if !found {
		room.Members = append(room.Members, models.WatchRoomMember{ProfileID: profileID, Name: f.names[profileID], Joined: true, ClientID: clientID})
	}
	f.seen[roomID+"|"+profileID] = time.Now()
	f.mu.Unlock()
	return f.Get(ctx, roomID, profileID)
}

func (f *fakeRooms) SetReady(ctx context.Context, roomID, profileID string, ready bool) (*models.WatchRoom, error) {
	f.mu.Lock()
	room := f.rooms[roomID]
	all := true
	for i := range room.Members {
		if room.Members[i].ProfileID == profileID {
			room.Members[i].Ready = ready
		}
		all = all && room.Members[i].Ready
	}
	if room.WaitingForReady && all {
		room.WaitingForReady, room.AnchorUpdatedAt = false, time.Now().UTC()
		room.Revision++
	}
	f.mu.Unlock()
	return f.Get(ctx, roomID, profileID)
}

func (f *fakeRooms) UpdateState(ctx context.Context, roomID, profileID string, u models.WatchRoomStateUpdate) (*models.WatchRoom, error) {
	f.mu.Lock()
	room := f.rooms[roomID]
	if f.requireMember && !f.joined(room, profileID) {
		f.mu.Unlock()
		return nil, errors.New("profile has not joined this watch room")
	}
	if u.ExpectedRevision != nil && *u.ExpectedRevision != room.Revision {
		f.mu.Unlock()
		return nil, errors.New("watch room state changed before this update")
	}
	if room.Status == models.WatchRoomStatusLobby && u.Status == models.WatchRoomStatusPlaying {
		room.WaitingForReady = true
		for i := range room.Members {
			room.Members[i].Ready = false
		}
	}
	room.Status, room.Position, room.AnchorUpdatedAt = u.Status, u.Position, time.Now().UTC()
	room.Revision++
	f.mu.Unlock()
	return f.Get(ctx, roomID, profileID)
}

func (f *fakeRooms) Heartbeat(_ context.Context, roomID, profileID, clientID string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beats++
	f.seen[roomID+"|"+profileID] = time.Now()
	if room := f.rooms[roomID]; room != nil {
		for i := range room.Members {
			if room.Members[i].ProfileID == profileID {
				room.Members[i].ClientID = clientID
			}
		}
	}
	return nil
}

func (f *fakeRooms) Leave(_ context.Context, roomID, profileID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	room := f.rooms[roomID]
	for i := range room.Members {
		if room.Members[i].ProfileID == profileID && !room.Members[i].IsCreator {
			room.Members = append(room.Members[:i], room.Members[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeRooms) End(_ context.Context, roomID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rooms[roomID].Status = models.WatchRoomStatusEnded
	f.rooms[roomID].Revision++
	return nil
}

type socketMsg struct {
	MessageType string
	Data        json.RawMessage
}

// syncClient captures what the bridge pushes to one Jellyfin session.
type syncClient struct {
	t *testing.T
	c *socketClient
}

func (c *syncClient) drain() []socketMsg {
	c.t.Helper()
	var out []socketMsg
	for {
		select {
		case f := <-c.c.out:
			var m socketMsg
			if err := json.Unmarshal(f.payload, &m); err != nil {
				c.t.Fatal(err)
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

// expect returns the messages' group update types and commands, in order.
func (c *syncClient) expect() ([]string, []syncCommand, []json.RawMessage) {
	var kinds []string
	var cmds []syncCommand
	var data []json.RawMessage
	for _, m := range c.drain() {
		switch m.MessageType {
		case "SyncPlayGroupUpdate":
			var u struct {
				Type string
				Data json.RawMessage
			}
			_ = json.Unmarshal(m.Data, &u)
			kinds = append(kinds, u.Type)
			data = append(data, u.Data)
		case "SyncPlayCommand":
			var cmd syncCommand
			_ = json.Unmarshal(m.Data, &cmd)
			cmds = append(cmds, cmd)
		}
	}
	return kinds, cmds, data
}

func syncHarness(t *testing.T) (*harness, *fakeRooms, string, *syncClient) {
	t.Helper()
	h := newHarness(t, config.JellyfinBridgeSettings{})
	rooms := newFakeRooms()
	h.srv.deps.WatchRooms = rooms
	rooms.names[h.alice.ID] = "Living Room"
	rooms.names["host"] = "Host"
	tok := h.login("Living Room", "alice-pass")
	// jellyfin-web's SDK socket names no device, only its token.
	client := &syncClient{t: t, c: h.srv.sockets.add(h.alice.ID, "", tok, nil)}
	return h, rooms, tok, client
}

func near(ticks int64, seconds float64) bool {
	return math.Abs(float64(ticks)/ticksPerSecond-seconds) < 1.5
}

// A room started in the mediastorm app: a Jellyfin client lists it, joins,
// loads the title, follows the room, and its pause reaches the room.
func TestSyncPlayJoinsAppRoom(t *testing.T) {
	h, rooms, tok, alice := syncHarness(t)
	me := decode[dto.UserDto](t, h.do(http.MethodGet, "/Users/Me", tok, nil))
	if me.Policy == nil || me.Policy.SyncPlayAccess != "CreateAndJoinGroups" {
		t.Fatalf("SyncPlayAccess = %+v", me.Policy)
	}
	room := rooms.add("host", models.WatchRoom{Title: "The Matrix", MediaType: "movie", ItemID: "tmdb:movie:603",
		Params: map[string]string{"tmdbId": "603", "title": "The Matrix"}, Status: models.WatchRoomStatusPlaying, Position: 100}, h.alice.ID)
	rooms.add("host", models.WatchRoom{Title: "Not for alice", MediaType: "movie", ItemID: "tmdb:movie:1"})

	groups := decode[[]groupInfo](t, h.do(http.MethodGet, "/SyncPlay/List", tok, nil))
	if len(groups) != 1 || groups[0].GroupID != compactID(room.ID) || groups[0].State != "Playing" || groups[0].GroupName != "Host: The Matrix" {
		t.Fatalf("groups = %+v", groups)
	}
	if rec := h.do(http.MethodPost, "/SyncPlay/Join", tok, map[string]string{"GroupId": groups[0].GroupID}); rec.Code != http.StatusNoContent {
		t.Fatalf("join: %d", rec.Code)
	}
	kinds, cmds, data := alice.expect()
	if fmt.Sprint(kinds) != "[GroupJoined PlayQueue StateUpdate]" || len(cmds) != 0 {
		t.Fatalf("after join: %v %v", kinds, cmds)
	}
	var queue playQueueUpdate
	_ = json.Unmarshal(data[1], &queue)
	if queue.Reason != "NewPlaylist" || len(queue.Playlist) != 1 || !queue.IsPlaying || !near(queue.StartPositionTicks, 100) {
		t.Fatalf("queue = %+v", queue)
	}
	if item := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+queue.Playlist[0].ItemID, tok, nil)); item.Name != "The Matrix" || item.Type != dto.KindMovie {
		t.Fatalf("queued item = %+v", item)
	}
	if got, _ := rooms.Get(context.Background(), room.ID, h.alice.ID); len(got.Members) != 2 || got.Members[1].ClientID != "jellyfin-dev1" {
		t.Fatalf("room members = %+v", got.Members)
	}

	// Loaded, but behind the room (loading took time): it seeks a little
	// ahead of the room…
	h.do(http.MethodPost, "/SyncPlay/Ready", tok, map[string]any{"PositionTicks": 0, "IsPlaying": false})
	_, cmds, _ = alice.expect()
	if len(cmds) != 1 || cmds[0].Command != "Seek" || !near(cmds[0].PositionTicks, 103) || cmds[0].PlaylistItemID != queue.Playlist[0].PlaylistItemID {
		t.Fatalf("catch-up seek = %+v", cmds)
	}
	// …and, holding there, starts when the room reaches it.
	h.do(http.MethodPost, "/SyncPlay/Ready", tok, map[string]any{"PositionTicks": cmds[0].PositionTicks, "IsPlaying": false})
	_, cmds, _ = alice.expect()
	if len(cmds) != 1 || cmds[0].Command != "Unpause" || !near(cmds[0].PositionTicks, 103) || cmds[0].When.Before(time.Now().Add(2*time.Second)) || cmds[0].When.After(time.Now().Add(4*time.Second)) {
		t.Fatalf("timed start = %+v", cmds)
	}
	// jellyfin-web's second Ready, sent while it briefly plays during the
	// seek, is 3 s off but must not start another seek (that looped live).
	h.do(http.MethodPost, "/SyncPlay/Ready", tok, map[string]any{"When": time.Now().UTC(), "PositionTicks": secondsToTicks(ticksToSeconds(cmds[0].PositionTicks)), "IsPlaying": true})
	if _, more, _ := alice.expect(); len(more) != 0 {
		t.Fatalf("ready while seeking = %+v", more)
	}
	// Playing in step: a further Ready (after buffering) changes nothing.
	h.do(http.MethodPost, "/SyncPlay/Ready", tok, map[string]any{"When": time.Now().UTC(), "PositionTicks": secondsToTicks(roomPosition(rooms.view(rooms.rooms[room.ID]), time.Now())), "IsPlaying": true})
	if _, cmds, _ = alice.expect(); len(cmds) != 0 {
		t.Fatalf("in-step ready = %+v", cmds)
	}

	// Pausing from Jellyfin pauses the room for everyone.
	h.do(http.MethodPost, "/SyncPlay/Pause", tok, nil)
	if got, _ := rooms.Get(context.Background(), room.ID, "host"); got.Status != models.WatchRoomStatusPaused {
		t.Fatalf("room status = %s", got.Status)
	}
	if _, cmds, _ = alice.expect(); len(cmds) != 1 || cmds[0].Command != "Pause" {
		t.Fatalf("pause command = %+v", cmds)
	}

	// The host resumes in the app where the room paused: a plain unpause.
	paused, _ := rooms.Get(context.Background(), room.ID, "host")
	if _, err := rooms.UpdateState(context.Background(), room.ID, "host", models.WatchRoomStateUpdate{Status: models.WatchRoomStatusPlaying, Position: paused.Position}); err != nil {
		t.Fatal(err)
	}
	h.srv.syncTick(context.Background())
	if _, cmds, _ = alice.expect(); len(cmds) != 1 || cmds[0].Command != "Unpause" || !near(cmds[0].PositionTicks, paused.Position+0.4) {
		t.Fatalf("app resume = %+v", cmds)
	}

	// The host jumps to 50s in the app: Jellyfin members seek there.
	if _, err := rooms.UpdateState(context.Background(), room.ID, "host", models.WatchRoomStateUpdate{Status: models.WatchRoomStatusPlaying, Position: 50}); err != nil {
		t.Fatal(err)
	}
	h.srv.syncTick(context.Background())
	if _, cmds, _ = alice.expect(); len(cmds) != 1 || cmds[0].Command != "Seek" || !near(cmds[0].PositionTicks, 53) {
		t.Fatalf("app jump = %+v", cmds)
	}

	// A seek from Jellyfin moves the room and seeks its members.
	h.do(http.MethodPost, "/SyncPlay/Seek", tok, map[string]any{"PositionTicks": int64(600 * ticksPerSecond)})
	if got, _ := rooms.Get(context.Background(), room.ID, "host"); math.Abs(got.Position-600) > 1.5 || got.Status != models.WatchRoomStatusPlaying {
		t.Fatalf("after seek: %+v", got)
	}
	if _, cmds, _ = alice.expect(); len(cmds) != 1 || cmds[0].Command != "Seek" || !near(cmds[0].PositionTicks, 603) {
		t.Fatalf("seek commands = %+v", cmds)
	}

	// Ending the room in the app ends the group.
	_ = rooms.End(context.Background(), room.ID, "host")
	h.srv.syncTick(context.Background())
	if kinds, _, _ := alice.expect(); fmt.Sprint(kinds) != "[GroupLeft]" {
		t.Fatalf("after end: %v", kinds)
	}
	if h.srv.syncPlayState().member(&principal{Profile: h.alice, Client: clientInfo{DeviceID: "dev1"}}) != nil {
		t.Fatal("member kept after the room ended")
	}
}

// A group made in Jellyfin becomes a room with its first queue; the
// creator's ready starts it.
func TestSyncPlayGroupFromJellyfin(t *testing.T) {
	h, rooms, tok, alice := syncHarness(t)
	movie := movieID(t, h, tok)
	info := decode[groupInfo](t, h.do(http.MethodPost, "/SyncPlay/New", tok, map[string]string{"GroupName": "Movie night"}))
	if info.GroupName != "Movie night" || info.State != "Idle" {
		t.Fatalf("new group = %+v", info)
	}
	if kinds, _, _ := alice.expect(); fmt.Sprint(kinds) != "[GroupJoined]" {
		t.Fatalf("after new: %v", kinds)
	}
	if rec := h.do(http.MethodPost, "/SyncPlay/SetNewQueue", tok, map[string]any{"PlayingQueue": []string{movie}, "PlayingItemPosition": 0, "StartPositionTicks": 0}); rec.Code != http.StatusNoContent {
		t.Fatalf("set queue: %d %s", rec.Code, rec.Body.String())
	}
	if len(rooms.created) != 1 || rooms.created[0].MediaType != "movie" || rooms.created[0].ItemID == "" || rooms.created[0].Params["tmdbId"] != "603" || rooms.created[0].ClientID != "jellyfin-dev1" {
		t.Fatalf("created = %+v", rooms.created)
	}
	kinds, _, data := alice.expect()
	if fmt.Sprint(kinds) != "[PlayQueue StateUpdate]" {
		t.Fatalf("after queue: %v", kinds)
	}
	var queue playQueueUpdate
	_ = json.Unmarshal(data[0], &queue)
	if len(queue.Playlist) != 1 || queue.Playlist[0].ItemID != movie || queue.IsPlaying {
		t.Fatalf("queue = %+v", queue)
	}

	h.do(http.MethodPost, "/SyncPlay/Ready", tok, map[string]any{})
	var room *models.WatchRoom
	for _, r := range rooms.rooms {
		room = r
	}
	if room.Status != models.WatchRoomStatusPlaying || room.WaitingForReady {
		t.Fatalf("room after creator ready: %+v", room)
	}
	if _, cmds, _ := alice.expect(); len(cmds) == 0 || cmds[len(cmds)-1].Command != "Unpause" || cmds[len(cmds)-1].GroupID != info.GroupID {
		t.Fatalf("start commands = %+v", cmds)
	}

	// The creator leaving ends the room.
	h.do(http.MethodPost, "/SyncPlay/Leave", tok, nil)
	if room.Status != models.WatchRoomStatusEnded {
		t.Fatalf("room after creator left: %s", room.Status)
	}
	if kinds, _, _ := alice.expect(); fmt.Sprint(kinds) != "[GroupLeft]" {
		t.Fatalf("after leave: %v", kinds)
	}
}

// A member of an app room cannot start it from its lobby (the host does).
func TestSyncPlayLobbyWaitsForHost(t *testing.T) {
	h, rooms, tok, alice := syncHarness(t)
	room := rooms.add("host", models.WatchRoom{Title: "The Matrix", MediaType: "movie", ItemID: "tmdb:movie:603", Params: map[string]string{"tmdbId": "603"}}, h.alice.ID)
	h.do(http.MethodPost, "/SyncPlay/Join", tok, map[string]string{"GroupId": compactID(room.ID)})
	h.do(http.MethodPost, "/SyncPlay/Ready", tok, map[string]any{})
	h.do(http.MethodPost, "/SyncPlay/Unpause", tok, nil)
	if got, _ := rooms.Get(context.Background(), room.ID, "host"); got.Status != models.WatchRoomStatusLobby {
		t.Fatalf("room started by a guest: %s", got.Status)
	}
	alice.drain()
	// The host starts it: the room waits for everyone. The Jellyfin member
	// already has the title loaded, so the watcher marks it ready and the
	// room plays once the host's app is ready too.
	if _, err := rooms.UpdateState(context.Background(), room.ID, "host", models.WatchRoomStateUpdate{Status: models.WatchRoomStatusPlaying}); err != nil {
		t.Fatal(err)
	}
	h.srv.syncTick(context.Background())
	if got, _ := rooms.Get(context.Background(), room.ID, "host"); !got.WaitingForReady || !got.Members[1].Ready {
		t.Fatalf("room while the host loads: %+v", got)
	}
	if _, cmds, _ := alice.expect(); len(cmds) != 1 || cmds[0].Command != "Pause" {
		t.Fatalf("waiting command = %+v", cmds)
	}
	_, _ = rooms.SetReady(context.Background(), room.ID, "host", true)
	h.srv.syncTick(context.Background())
	if got, _ := rooms.Get(context.Background(), room.ID, "host"); got.WaitingForReady || got.Status != models.WatchRoomStatusPlaying {
		t.Fatalf("room after all ready: %+v", got)
	}
	if _, cmds, _ := alice.expect(); len(cmds) != 1 || cmds[0].Command != "Unpause" {
		t.Fatalf("release command = %+v", cmds)
	}
}

// A member that stays behind (slow to load) gets at most 3 catch-up seeks,
// then plays along from where the room is.
func TestSyncPlayCatchUpIsBounded(t *testing.T) {
	h, rooms, tok, alice := syncHarness(t)
	room := rooms.add("host", models.WatchRoom{Title: "The Matrix", MediaType: "movie", ItemID: "tmdb:movie:603", Params: map[string]string{"tmdbId": "603"},
		Status: models.WatchRoomStatusPlaying, Position: 100}, h.alice.ID)
	h.do(http.MethodPost, "/SyncPlay/Join", tok, map[string]string{"GroupId": compactID(room.ID)})
	alice.drain()
	var got []string
	for range 5 {
		h.do(http.MethodPost, "/SyncPlay/Ready", tok, map[string]any{"PositionTicks": 0, "IsPlaying": false})
		_, cmds, _ := alice.expect()
		for _, c := range cmds {
			got = append(got, c.Command)
		}
	}
	if fmt.Sprint(got) != "[Seek Seek Seek Unpause Unpause]" {
		t.Fatalf("commands = %v", got)
	}
}

func TestSyncPlayEpisodeRoomAndUtcTime(t *testing.T) {
	h, rooms, tok, alice := syncHarness(t)
	room := rooms.add("host", models.WatchRoom{Title: "Game of Thrones", MediaType: "episode", ItemID: "tmdb:tv:1399",
		Params: map[string]string{"tmdbId": "1399", "seasonNumber": "1", "episodeNumber": "2", "seriesTitle": "Game of Thrones"}}, h.alice.ID)
	h.do(http.MethodPost, "/SyncPlay/Join", tok, map[string]string{"GroupId": compactID(room.ID)})
	_, _, data := alice.expect()
	var queue playQueueUpdate
	_ = json.Unmarshal(data[1], &queue)
	item := decode[dto.BaseItemDto](t, h.do(http.MethodGet, "/Items/"+queue.Playlist[0].ItemID, tok, nil))
	if item.Type != dto.KindEpisode || item.ParentIndexNumber == nil || *item.ParentIndexNumber != 1 || item.IndexNumber == nil || *item.IndexNumber != 2 {
		t.Fatalf("episode item = %+v", item)
	}
	utc := decode[map[string]string](t, h.do(http.MethodGet, "/GetUtcTime", "", nil))
	if _, err := time.Parse(time.RFC3339Nano, utc["ResponseTransmissionTime"]); err != nil {
		t.Fatalf("utc time = %+v", utc)
	}
}

// With stream resolution available, the queue goes out once the stream is
// resolved: jellyfin-web gives up on a group whose playback has not started
// 30 s after the queue arrived.
func TestSyncPlayQueueWaitsForStream(t *testing.T) {
	h, rooms, tok, alice := syncHarness(t)
	installFakePlayback(t, h)
	room := rooms.add("host", models.WatchRoom{Title: "The Matrix", MediaType: "movie", ItemID: "tmdb:movie:603", Params: map[string]string{"tmdbId": "603"}}, h.alice.ID)
	h.do(http.MethodPost, "/SyncPlay/Join", tok, map[string]string{"GroupId": compactID(room.ID)})
	var kinds []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		k, _, _ := alice.expect()
		kinds = append(kinds, k...)
		if fmt.Sprint(kinds) != "[GroupJoined StateUpdate]" && len(kinds) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if fmt.Sprint(kinds) != "[GroupJoined StateUpdate PlayQueue]" {
		t.Fatalf("messages = %v", kinds)
	}
}

// A host who joined their app-made room from Jellyfin and leaves the group
// there may still be watching in the app: the room keeps running.
func TestSyncPlayHostLeavingKeepsAppRoom(t *testing.T) {
	h, rooms, tok, alice := syncHarness(t)
	room := rooms.add(h.alice.ID, models.WatchRoom{Title: "The Matrix", MediaType: "movie", ItemID: "tmdb:movie:603", Params: map[string]string{"tmdbId": "603"},
		Status: models.WatchRoomStatusPlaying, Position: 10})
	h.do(http.MethodPost, "/SyncPlay/Join", tok, map[string]string{"GroupId": compactID(room.ID)})
	h.do(http.MethodPost, "/SyncPlay/Leave", tok, nil)
	if got, _ := rooms.Get(context.Background(), room.ID, h.alice.ID); got.Status == models.WatchRoomStatusEnded {
		t.Fatal("leaving the Jellyfin group ended the host's app room")
	}
	if kinds, _, _ := alice.expect(); len(kinds) == 0 || kinds[len(kinds)-1] != "GroupLeft" {
		t.Fatalf("after leave: %v", kinds)
	}
}

// A group made in Jellyfin invites the household, but not its kids profiles.
func TestSyncPlayDoesNotInviteKids(t *testing.T) {
	h, rooms, tok, _ := syncHarness(t)
	kid, err := h.users.CreateForAccount(h.alice.AccountID, "Kid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.users.SetKidsProfile(kid.ID, true); err != nil {
		t.Fatal(err)
	}
	adult, err := h.users.CreateForAccount(h.alice.AccountID, "Adult")
	if err != nil {
		t.Fatal(err)
	}
	h.do(http.MethodPost, "/SyncPlay/New", tok, map[string]string{"GroupName": "g"})
	h.do(http.MethodPost, "/SyncPlay/SetNewQueue", tok, map[string]any{"PlayingQueue": []string{movieID(t, h, tok)}})
	if len(rooms.created) != 1 {
		t.Fatalf("created = %d", len(rooms.created))
	}
	invited := fmt.Sprint(rooms.created[0].InviteeProfileIDs)
	if !strings.Contains(invited, adult.ID) || strings.Contains(invited, kid.ID) {
		t.Fatalf("invitees = %v (kid %s, adult %s)", invited, kid.ID, adult.ID)
	}
}

// When the profile's membership was given up elsewhere (the app left), a
// pause from the Jellyfin session takes it back instead of being lost.
func TestSyncPlayReclaimsMembership(t *testing.T) {
	h, rooms, tok, _ := syncHarness(t)
	room := rooms.add("host", models.WatchRoom{Title: "The Matrix", MediaType: "movie", ItemID: "tmdb:movie:603", Params: map[string]string{"tmdbId": "603"},
		Status: models.WatchRoomStatusPlaying, Position: 10}, h.alice.ID)
	rooms.requireMember = true
	h.do(http.MethodPost, "/SyncPlay/Join", tok, map[string]string{"GroupId": compactID(room.ID)})
	_ = rooms.Leave(context.Background(), room.ID, h.alice.ID)
	h.do(http.MethodPost, "/SyncPlay/Pause", tok, nil)
	got, _ := rooms.Get(context.Background(), room.ID, "host")
	if got.Status != models.WatchRoomStatusPaused || len(got.Members) != 2 {
		t.Fatalf("room after pause = %s, members %d", got.Status, len(got.Members))
	}
}

// The same profile in the mediastorm app and in Jellyfin: the bridge does not
// take over the membership the app claimed, and leaving the Jellyfin group
// does not remove the app from the room.
func TestSyncPlayKeepsAppMembership(t *testing.T) {
	h, rooms, tok, _ := syncHarness(t)
	room := rooms.add("host", models.WatchRoom{Title: "The Matrix", MediaType: "movie", ItemID: "tmdb:movie:603", Params: map[string]string{"tmdbId": "603"},
		Status: models.WatchRoomStatusPlaying, Position: 10}, h.alice.ID)
	h.do(http.MethodPost, "/SyncPlay/Join", tok, map[string]string{"GroupId": compactID(room.ID)})
	// The app (same profile) claims the membership.
	_ = rooms.Heartbeat(context.Background(), room.ID, h.alice.ID, "app-phone", false)
	h.do(http.MethodPost, "/SyncPlay/Ready", tok, map[string]any{})
	got, _ := rooms.Get(context.Background(), room.ID, "host")
	if got.Members[1].ClientID != "app-phone" {
		t.Fatalf("bridge took over the app's membership: %q", got.Members[1].ClientID)
	}
	h.do(http.MethodPost, "/SyncPlay/Leave", tok, nil)
	if got, _ := rooms.Get(context.Background(), room.ID, "host"); len(got.Members) != 2 {
		t.Fatalf("leaving the Jellyfin group removed the app: %+v", got.Members)
	}
}

// An app that quit without leaving stops holding the membership once its
// presence lapses: the bridge takes it over and gives it up on leave, so no
// absent member is left to block the room.
func TestSyncPlayTakesOverLapsedAppMembership(t *testing.T) {
	h, rooms, tok, _ := syncHarness(t)
	room := rooms.add("host", models.WatchRoom{Title: "The Matrix", MediaType: "movie", ItemID: "tmdb:movie:603", Params: map[string]string{"tmdbId": "603"},
		Status: models.WatchRoomStatusPlaying, Position: 10}, h.alice.ID)
	h.do(http.MethodPost, "/SyncPlay/Join", tok, map[string]string{"GroupId": compactID(room.ID)})
	_ = rooms.Heartbeat(context.Background(), room.ID, h.alice.ID, "app-phone", false)
	rooms.mu.Lock()
	rooms.seen[room.ID+"|"+h.alice.ID] = time.Now().Add(-time.Minute) // the app went away
	rooms.mu.Unlock()
	h.do(http.MethodPost, "/SyncPlay/Ready", tok, map[string]any{})
	if got, _ := rooms.Get(context.Background(), room.ID, "host"); got.Members[1].ClientID != "jellyfin-dev1" {
		t.Fatalf("lapsed app membership not taken over: %q", got.Members[1].ClientID)
	}
	h.do(http.MethodPost, "/SyncPlay/Leave", tok, nil)
	if got, _ := rooms.Get(context.Background(), room.ID, "host"); len(got.Members) != 1 {
		t.Fatalf("absent member left behind: %+v", got.Members)
	}
}

// A member whose profile lost access to a room leaves alone: the room keeps
// running for the members who can still see it, whichever is checked first.
func TestSyncPlayLostAccessLeavesOnlyThatProfile(t *testing.T) {
	h, rooms, tok, alice := syncHarness(t)
	den, err := h.users.CreateForAccount(h.alice.AccountID, "Den")
	if err != nil {
		t.Fatal(err)
	}
	rooms.names[den.ID] = "Den"
	denTok := h.login("Den", "alice-pass")
	denClient := &syncClient{t: t, c: h.srv.sockets.add(den.ID, "", denTok, nil)}
	room := rooms.add("host", models.WatchRoom{Title: "The Matrix", MediaType: "movie", ItemID: "tmdb:movie:603",
		Params: map[string]string{"tmdbId": "603"}, Status: models.WatchRoomStatusPlaying, Position: 10}, h.alice.ID, den.ID)
	for _, tk := range []string{tok, denTok} {
		if rec := h.do(http.MethodPost, "/SyncPlay/Join", tk, map[string]string{"GroupId": compactID(room.ID)}); rec.Code != http.StatusNoContent {
			t.Fatalf("join: %d", rec.Code)
		}
	}
	alice.drain()
	denClient.drain()

	rooms.mu.Lock()
	rooms.invites[room.ID] = []string{h.alice.ID}
	rooms.mu.Unlock()
	h.srv.syncTick(context.Background())

	if kinds, _, _ := denClient.expect(); !strings.Contains(fmt.Sprint(kinds), "GroupLeft") {
		t.Fatalf("den after losing access: %v", kinds)
	}
	if kinds, _, _ := alice.expect(); strings.Contains(fmt.Sprint(kinds), "GroupLeft") {
		t.Fatalf("alice dropped with den: %v", kinds)
	}
	if h.srv.syncPlayState().member(&principal{Profile: h.alice, Client: clientInfo{DeviceID: "dev1"}}) == nil {
		t.Fatal("alice's membership dropped")
	}
}
