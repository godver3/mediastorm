package jellyfinbridge

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

// Jellyfin SyncPlay on top of mediastorm watch rooms, so Jellyfin clients and
// the mediastorm apps watch together in the same room.
//
// The watch room stays the single source of truth: Jellyfin requests (pause,
// unpause, seek, ready, buffering) become room updates, and room changes,
// whoever made them, become SyncPlay commands pushed over the Jellyfin
// WebSocket. A watcher reads each room with Jellyfin members once a second
// (the mediastorm apps poll at the same rate) and heartbeats those members
// so the room sees them as present.
//
// Differences from Jellyfin's own SyncPlay, by design:
//   - Groups are the profile's watch rooms (created by it or inviting it); a
//     group created from a Jellyfin client becomes a room open to the
//     creator's household, and more people are invited from the mediastorm app.
//   - A room plays one title or episode: queue editing, next/previous,
//     shuffle and repeat are accepted and ignored.
//   - A member who joins late catches up instead of pausing everyone, and a
//     buffering member does not hold the group (as in the mediastorm apps).
//   - Only the room's creator starts a room that is still in its lobby.

// WatchRoomService is the part of mediastorm's watch rooms the bridge uses.
type WatchRoomService interface {
	Create(ctx context.Context, actorAccountID, creatorID string, in models.WatchRoomCreate) (*models.WatchRoom, error)
	Invitations(ctx context.Context, profileID string) ([]models.WatchRoom, error)
	Get(ctx context.Context, roomID, profileID string) (*models.WatchRoom, error)
	Join(ctx context.Context, roomID, profileID, clientID string, capabilities models.WatchRoomClientCapabilities) (*models.WatchRoom, error)
	SetReady(ctx context.Context, roomID, profileID string, ready bool) (*models.WatchRoom, error)
	UpdateState(ctx context.Context, roomID, profileID string, update models.WatchRoomStateUpdate) (*models.WatchRoom, error)
	Heartbeat(ctx context.Context, roomID, profileID, clientID string, buffering bool) error
	Leave(ctx context.Context, roomID, profileID string) error
	End(ctx context.Context, roomID, profileID string) error
}

// syncPlayCapabilities is what a bridge member tells the room it can do: it
// plays natively and follows the shared state.
var syncPlayCapabilities = models.WatchRoomClientCapabilities{NativePlayback: true, StateSync: true, ProtocolVersion: 1}

const (
	syncPlayTick        = time.Second
	syncPlayHeartbeat   = 5 * time.Second
	syncPlayLead        = 400 * time.Millisecond // time clients get to act on a command
	syncPlaySocketGrace = 45 * time.Second       // a member without a socket this long leaves
	// syncPlaySeekLead is how far ahead of the room a catching-up member
	// seeks: it then holds until the room gets there (jellyfin-web only
	// corrects a position on its own when it is ahead).
	syncPlaySeekLead  = 3 * time.Second
	syncPlaySeekTries = 3               // catch-up seeks before a member just plays along
	syncPlayJump      = 1.5             // seconds a room may move unexpectedly before members seek
	syncPlayDrift     = 2.0             // seconds a playing member may be off before it is corrected
	syncPlayCooldown  = 5 * time.Second // after a command, a member settles before it is corrected again
	// syncPlayResolveWait bounds how long a joining member's queue waits
	// for its stream to resolve (it is sent anyway afterwards).
	syncPlayResolveWait = 60 * time.Second
	// syncPlayRetryAfter gives a member whose catch-up seeks ran out new
	// ones once it has been left alone this long.
	syncPlayRetryAfter = 30 * time.Second
	// syncPlayTickBudget bounds one watcher pass.
	syncPlayTickBudget = 10 * time.Second
)

// syncMember is one Jellyfin session (profile + device) in a group.
type syncMember struct {
	p *principal
	// groupID is the GroupId this session knows the group by: the room ID,
	// or for the creator of a group made in Jellyfin the ID it got before
	// the room existed.
	groupID   string
	roomID    string // "" until a group created in Jellyfin gets its queue
	groupName string
	itemID    string // the room's title or episode as a Jellyfin item
	playlist  string // PlaylistItemId of that single entry
	creator   bool
	// createdHere marks the creator of a room made from a Jellyfin group:
	// leaving ends that room, as in Jellyfin. A room made in the app keeps
	// running when its host leaves the Jellyfin group (the host may still
	// be watching in the app; the room's own sweep ends abandoned rooms).
	createdHere bool
	queueGen    int  // bumps with every queue sent, so a late one is dropped
	loaded      bool // reported Ready since joining: playback is loaded
	buffering   bool
	joinedAt    time.Time
	noSocket    time.Time // when the socket was first seen missing
	lastBeat    time.Time
	seekTries   int       // catch-up seeks since the member last played in step
	lastCmd     time.Time // when the last play/pause/seek command went to it
}

func memberKey(p *principal) string { return p.Profile.ID + "|" + p.Client.DeviceID }

// syncRoomView is what the watcher last told the members of a room.
type syncRoomView struct {
	revision int64
	names    map[string]bool
	// The room's last state as pushed: where it was at its anchor and
	// whether it was playing, to tell a seek from playback going on.
	playing bool
	pos     float64
	anchor  time.Time
}

// anchorPosition is the room's position at its anchor (rooms come back with
// the position at read time).
func anchorPosition(room *models.WatchRoom) float64 {
	if roomPlaying(room) {
		return room.Position - time.Since(room.AnchorUpdatedAt).Seconds()
	}
	return room.Position
}

func (v *syncRoomView) remember(room *models.WatchRoom) {
	v.revision, v.playing, v.pos, v.anchor = room.Revision, roomPlaying(room), anchorPosition(room), room.AnchorUpdatedAt
}

// jumped reports whether a playing room moved somewhere playback alone
// would not have taken it (a seek, or a resume at another position).
func (v *syncRoomView) jumped(room *models.WatchRoom) bool {
	if v == nil || !roomPlaying(room) {
		return false
	}
	expected := v.pos
	if v.playing {
		expected += room.AnchorUpdatedAt.Sub(v.anchor).Seconds()
	}
	d := anchorPosition(room) - expected
	return d > syncPlayJump || d < -syncPlayJump
}

type syncPlay struct {
	// op serialises everything that reads or changes members and pushes to
	// them (handlers, the watcher, late queues). SyncPlay traffic is small,
	// and one order of events keeps older room states from overtaking newer
	// ones. Waiting for streams happens outside it.
	op      sync.Mutex
	mu      sync.Mutex
	members map[string]*syncMember
	views   map[string]*syncRoomView
	started bool
	stop    chan struct{}
}

func (s *Server) syncPlayState() *syncPlay {
	s.syncOnce.Do(func() {
		s.sync = &syncPlay{members: map[string]*syncMember{}, views: map[string]*syncRoomView{}, stop: make(chan struct{})}
	})
	return s.sync
}

// syncPlayAccess is the user policy's SyncPlayAccess.
func (s *Server) syncPlayAccess(profile models.User) string {
	if s.deps.WatchRooms == nil || profile.IsKidsProfile {
		return "None"
	}
	return "CreateAndJoinGroups"
}

func (s *Server) syncPlayEnabled(p *principal) bool {
	return s.deps.WatchRooms != nil && p != nil && !p.Profile.IsKidsProfile
}

// --- DTOs ---------------------------------------------------------------------

type groupInfo struct {
	GroupID       string    `json:"GroupId"`
	GroupName     string    `json:"GroupName"`
	State         string    `json:"State"`
	Participants  []string  `json:"Participants"`
	LastUpdatedAt time.Time `json:"LastUpdatedAt"`
}

type groupUpdate struct {
	GroupID string `json:"GroupId"`
	Type    string `json:"Type"`
	Data    any    `json:"Data"`
}

type syncQueueItem struct {
	ItemID         string `json:"ItemId"`
	PlaylistItemID string `json:"PlaylistItemId"`
}

type playQueueUpdate struct {
	Reason             string          `json:"Reason"`
	LastUpdate         time.Time       `json:"LastUpdate"`
	Playlist           []syncQueueItem `json:"Playlist"`
	PlayingItemIndex   int             `json:"PlayingItemIndex"`
	StartPositionTicks int64           `json:"StartPositionTicks"`
	IsPlaying          bool            `json:"IsPlaying"`
	ShuffleMode        string          `json:"ShuffleMode"`
	RepeatMode         string          `json:"RepeatMode"`
}

type syncCommand struct {
	GroupID        string    `json:"GroupId"`
	PlaylistItemID string    `json:"PlaylistItemId"`
	When           time.Time `json:"When"`
	PositionTicks  int64     `json:"PositionTicks"`
	Command        string    `json:"Command"`
	EmittedAt      time.Time `json:"EmittedAt"`
}

// groupState maps a room's state to Jellyfin's group states.
func groupState(room *models.WatchRoom) string {
	switch {
	case room == nil || room.Status == models.WatchRoomStatusLobby:
		return "Idle"
	case room.Status == models.WatchRoomStatusPlaying && room.WaitingForReady:
		return "Waiting"
	case room.Status == models.WatchRoomStatusPlaying:
		return "Playing"
	}
	return "Paused"
}

func roomPlaying(room *models.WatchRoom) bool {
	return room.Status == models.WatchRoomStatusPlaying && !room.WaitingForReady
}

func joinedNames(room *models.WatchRoom) []string {
	names := []string{}
	for _, m := range room.Members {
		if m.Joined {
			names = append(names, firstNonEmpty(m.Name, "Guest"))
		}
	}
	return names
}

func (s *Server) groupInfoFor(m *syncMember, room *models.WatchRoom) groupInfo {
	info := groupInfo{GroupID: m.groupID, GroupName: m.groupName, State: groupState(room), Participants: []string{m.p.Profile.Name}, LastUpdatedAt: m.joinedAt}
	if room != nil {
		info.Participants = joinedNames(room)
	}
	return info
}

func roomGroupName(room *models.WatchRoom) string {
	if room.CreatorName == "" {
		return room.Title
	}
	return room.CreatorName + ": " + room.Title
}

// --- room ↔ item -------------------------------------------------------------

// roomItemID is the room's title or episode as a Jellyfin item ID, when the
// profile may see it.
func (s *Server) roomItemID(ctx context.Context, p *principal, room *models.WatchRoom) (string, bool) {
	param := func(k string) string { return strings.TrimSpace(room.Params[k]) }
	ext := map[string]string{}
	for k, name := range map[string]string{"tmdbId": "tmdb", "imdbId": "imdb", "tvdbId": "tvdb"} {
		if v := param(k); v != "" {
			ext[name] = v
		}
	}
	year, _ := strconv.Atoi(param("year"))
	u := s.newUserState(p.Profile.ID)
	switch strings.ToLower(room.MediaType) {
	case "movie":
		t := titleInfo{MediaType: "movie", ID: room.ItemID, Name: firstNonEmpty(param("title"), room.Title), Year: year, ExternalIDs: ext, Poster: room.PosterURL, Backdrop: room.BackdropURL}
		item := s.titleItem(u, t)
		ref, ok := s.ids.get(item.ID)
		return item.ID, ok && s.kidsAllowsRef(ctx, p.Profile, ref)
	case "episode", "series":
		season, _ := strconv.Atoi(param("seasonNumber"))
		episode, _ := strconv.Atoi(param("episodeNumber"))
		if episode <= 0 {
			return "", false
		}
		t := titleInfo{MediaType: "series", ID: room.ItemID, Name: firstNonEmpty(param("seriesTitle"), param("title"), room.Title), Year: year, ExternalIDs: ext, Poster: room.PosterURL, Backdrop: room.BackdropURL}
		series := s.seriesRefFrom(t)
		item := s.episodeItem(u, series, episodeInfo{Season: season, Episode: episode, Name: param("episodeName")})
		ref, ok := s.ids.get(item.ID)
		return item.ID, ok && s.kidsAllowsRef(ctx, p.Profile, ref)
	}
	return "", false
}

// roomCreateFor describes a Jellyfin movie or episode as a watch room, in the
// form the mediastorm apps create rooms (episodes by series ID plus season
// and episode).
func (s *Server) roomCreateFor(ctx context.Context, p *principal, itemID string) (models.WatchRoomCreate, bool) {
	ref, ok := s.ids.get(itemID)
	if !ok || !s.kidsAllowsRef(ctx, p.Profile, ref) {
		return models.WatchRoomCreate{}, false
	}
	// Marks rooms made from a Jellyfin group (see roomFromJellyfin).
	params := map[string]string{roomOriginParam: roomOriginJellyfin}
	setIDs := func(ext map[string]string) {
		for name, k := range map[string]string{"tmdb": "tmdbId", "imdb": "imdbId", "tvdb": "tvdbId"} {
			if v := ext[name]; v != "" {
				params[k] = v
			}
		}
	}
	switch ref.Kind {
	case refMovie:
		t := s.trustedTitle(ctx, "movie", ref.Source, titleFromRef(ref))
		params["mediaType"], params["title"], params["titleId"] = "movie", t.Name, ref.Source
		if t.Year > 0 {
			params["year"] = strconv.Itoa(t.Year)
		}
		setIDs(t.ExternalIDs)
		return models.WatchRoomCreate{Title: t.Name, MediaType: "movie", ItemID: ref.Source, PosterURL: t.Poster, BackdropURL: t.Backdrop, Params: params}, t.Name != ""
	case refEpisode:
		series := seriesInfoFromSeasonRef(ref)
		series.MediaType = "series"
		seriesID := series.source()
		name := firstNonEmpty(series.Name, ref.Ctx.SeriesName)
		params["mediaType"], params["title"], params["seriesTitle"], params["titleId"] = "episode", name, name, seriesID
		params["seasonNumber"], params["episodeNumber"] = strconv.Itoa(ref.Ctx.Season), strconv.Itoa(ref.Ctx.Episode)
		if ref.Ctx.Name != "" {
			params["episodeName"] = ref.Ctx.Name
		}
		setIDs(series.ExternalIDs)
		return models.WatchRoomCreate{Title: name, MediaType: "episode", ItemID: seriesID, PosterURL: firstNonEmpty(ref.Ctx.SeriesPoster, series.Poster), Params: params}, name != "" && ref.Ctx.Episode > 0
	}
	return models.WatchRoomCreate{}, false
}

// Rooms made from a Jellyfin group carry roomOriginParam, so their creator
// leaving the group ends them even after a restart or from another device.
const (
	roomOriginParam    = "createdBy"
	roomOriginJellyfin = "jellyfin"
)

func roomFromJellyfin(room *models.WatchRoom, profileID string) bool {
	return room != nil && room.CreatorProfileID == profileID && room.Params[roomOriginParam] == roomOriginJellyfin
}

// --- pushing to sessions -----------------------------------------------------

func (s *Server) pushGroup(m *syncMember, kind string, data any) {
	s.sockets.sendToDevice(m.p.Profile.ID, m.p.Client.DeviceID, m.p.Session.Token, "SyncPlayGroupUpdate", groupUpdate{GroupID: m.groupID, Type: kind, Data: data})
}

// pushQueue sends the room's single entry as a new playlist; the client
// loads it and reports Ready. jellyfin-web stops following the group when
// playback has not started 30 s after the queue arrives, and a cold start
// (search, pick, stream start) can take longer: the stream is resolved
// first, then the queue goes out with the room's position at that time.
//
// Called with sp.op held; the wait runs outside it.
func (s *Server) pushQueue(m *syncMember, room *models.WatchRoom) {
	m.queueGen++
	gen, roomID, itemID, p := m.queueGen, m.roomID, m.itemID, m.p
	ref, ok := s.ids.get(itemID)
	if !ok || (ref.Kind != refMovie && ref.Kind != refEpisode) || s.deps.Handlers.Prequeue == nil {
		s.sendQueue(m, room)
		return
	}
	sp := s.syncPlayState()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), syncPlayResolveWait+5*time.Second)
		defer cancel()
		if prequeueID, err := s.startPrequeue(ctx, p, ref, ""); err == nil {
			scratch := &playSession{ItemID: itemID, ProfileID: p.Profile.ID, prequeueID: prequeueID}
			s.awaitPrequeue(ctx, p, scratch, syncPlayResolveWait)
		}
		sp.op.Lock()
		defer sp.op.Unlock()
		// The session may have left, moved on or rejoined meanwhile: a late
		// queue would start the old title (jellyfin-web plays a queue even
		// outside a group).
		if sp.member(p) != m || m.queueGen != gen || m.roomID != roomID {
			return
		}
		if fresh, err := s.deps.WatchRooms.Get(ctx, roomID, p.Profile.ID); err == nil {
			room = fresh
		}
		if room == nil || room.Status == models.WatchRoomStatusEnded {
			return
		}
		s.sendQueue(m, room)
	}()
}

func (s *Server) sendQueue(m *syncMember, room *models.WatchRoom) {
	if room == nil {
		return
	}
	now := time.Now().UTC()
	s.pushGroup(m, "PlayQueue", playQueueUpdate{
		Reason: "NewPlaylist", LastUpdate: now, Playlist: []syncQueueItem{{ItemID: m.itemID, PlaylistItemID: m.playlist}},
		StartPositionTicks: secondsToTicks(roomPosition(room, now)), IsPlaying: roomPlaying(room),
		ShuffleMode: "Sorted", RepeatMode: "RepeatNone",
	})
}

// roomPosition is the room's position at a moment (rooms come back with
// the position at read time).
func roomPosition(room *models.WatchRoom, at time.Time) float64 {
	if room == nil {
		return 0
	}
	pos := room.Position
	if roomPlaying(room) {
		pos += at.Sub(time.Now()).Seconds()
		if room.Duration > 0 && pos > room.Duration {
			pos = room.Duration
		}
	}
	if pos < 0 {
		return 0
	}
	return pos
}

// pushCommand tells a loaded member what to do now: play from where the
// room will be shortly, or hold at its position.
func (s *Server) pushCommand(m *syncMember, room *models.WatchRoom) {
	if !m.loaded || room == nil || room.Status == models.WatchRoomStatusEnded {
		return
	}
	now := time.Now().UTC()
	cmd := syncCommand{GroupID: m.groupID, PlaylistItemID: m.playlist, EmittedAt: now}
	if roomPlaying(room) {
		cmd.Command, cmd.When = "Unpause", now.Add(syncPlayLead)
	} else {
		cmd.Command, cmd.When = "Pause", now
	}
	cmd.PositionTicks = secondsToTicks(roomPosition(room, cmd.When))
	m.lastCmd = now
	s.sockets.sendToDevice(m.p.Profile.ID, m.p.Client.DeviceID, m.p.Session.Token, "SyncPlayCommand", cmd)
}

// pushSeek sends a member slightly ahead of a playing room; it pauses there
// and reports Ready, and alignMember then starts it when the room arrives.
func (s *Server) pushSeek(m *syncMember, room *models.WatchRoom) {
	if !m.loaded {
		return
	}
	now := time.Now().UTC()
	// Each further try aims further ahead: a member that keeps arriving
	// late has a slow seek, and a bigger lead absorbs it.
	lead := syncPlaySeekLead * time.Duration(1+m.seekTries)
	m.seekTries++
	m.lastCmd = now
	cmd := syncCommand{GroupID: m.groupID, PlaylistItemID: m.playlist, When: now, Command: "Seek", EmittedAt: now,
		PositionTicks: secondsToTicks(roomPosition(room, now.Add(lead)))}
	s.sockets.sendToDevice(m.p.Profile.ID, m.p.Client.DeviceID, m.p.Session.Token, "SyncPlayCommand", cmd)
}

// alignMember answers a member's Ready (it reports where it is): a member
// holding ahead of a playing room starts when the room reaches it, one
// behind seeks ahead first, one already in step is left alone.
func (s *Server) alignMember(m *syncMember, room *models.WatchRoom, clientPos float64, playing bool) {
	now := time.Now().UTC()
	if !roomPlaying(room) {
		if playing || math.Abs(clientPos-room.Position) > 0.5 {
			s.pushCommand(m, room)
		}
		return
	}
	diff := clientPos - roomPosition(room, now)
	if now.Sub(m.lastCmd) > syncPlayRetryAfter {
		// Catch-up seeks are limited per jump, not forever: a member left
		// behind after its tries gets new ones later.
		m.seekTries = 0
	}
	if playing {
		// jellyfin-web also reports Ready while it briefly plays during a
		// seek or after buffering: leave a playing member alone unless it
		// is clearly off and nothing was sent to it just now.
		switch {
		case math.Abs(diff) < 0.5:
			m.seekTries = 0
		case math.Abs(diff) > syncPlayDrift && now.Sub(m.lastCmd) > syncPlayCooldown && m.seekTries < syncPlaySeekTries:
			s.pushSeek(m, room)
		}
		return
	}
	switch {
	case diff > 0.15 && diff < 30:
		// Holding ahead of the room: start when the room gets there.
		m.lastCmd = now
		cmd := syncCommand{GroupID: m.groupID, PlaylistItemID: m.playlist, When: now.Add(time.Duration(diff * float64(time.Second))),
			PositionTicks: secondsToTicks(clientPos), Command: "Unpause", EmittedAt: now}
		s.sockets.sendToDevice(m.p.Profile.ID, m.p.Client.DeviceID, m.p.Session.Token, "SyncPlayCommand", cmd)
	case m.seekTries < syncPlaySeekTries:
		s.pushSeek(m, room)
	default:
		// Out of catch-up seeks: play along from where the room is.
		s.pushCommand(m, room)
	}
}

func (s *Server) pushState(m *syncMember, room *models.WatchRoom, reason string) {
	s.pushGroup(m, "StateUpdate", map[string]string{"State": groupState(room), "Reason": reason})
}

// roomMembers are the bridge sessions in a room.
func (sp *syncPlay) roomMembers(roomID string) []*syncMember {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	var out []*syncMember
	for _, m := range sp.members {
		if m.roomID == roomID {
			out = append(out, m)
		}
	}
	return out
}

// broadcastRoom pushes the room's state to all its bridge sessions: a seek
// (or a jump made elsewhere) as catch-up seeks, anything else as the
// matching play or pause command.
func (s *Server) broadcastRoom(room *models.WatchRoom, reason string, seek bool) {
	sp := s.syncPlayState()
	sp.mu.Lock()
	v := sp.views[room.ID]
	if v != nil && room.Revision < v.revision {
		// An older read than what members already got.
		sp.mu.Unlock()
		return
	}
	jump := roomPlaying(room) && (seek || v.jumped(room))
	if v != nil {
		v.remember(room)
	}
	sp.mu.Unlock()
	for _, m := range sp.roomMembers(room.ID) {
		s.pushState(m, room, reason)
		if jump {
			m.seekTries = 0
			s.pushSeek(m, room)
		} else {
			s.pushCommand(m, room)
		}
	}
}

// --- membership --------------------------------------------------------------

func (sp *syncPlay) member(p *principal) *syncMember {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.members[memberKey(p)]
}

func (s *Server) addMember(m *syncMember, room *models.WatchRoom) {
	sp := s.syncPlayState()
	sp.mu.Lock()
	sp.members[memberKey(m.p)] = m
	if room != nil {
		names := map[string]bool{}
		for _, n := range joinedNames(room) {
			names[n] = true
		}
		if v := sp.views[room.ID]; v == nil {
			v = &syncRoomView{names: names}
			v.remember(room)
			sp.views[room.ID] = v
		} else {
			// The joiner is announced by the join itself, not again by
			// the watcher.
			v.names = names
		}
	}
	start := !sp.started
	sp.started = true
	sp.mu.Unlock()
	if start {
		go s.syncWatch(sp)
	}
}

// dropMember forgets a session; the room's view goes with its last member.
func (sp *syncPlay) dropMember(m *syncMember) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.members[memberKey(m.p)] != m {
		return
	}
	delete(sp.members, memberKey(m.p))
	for _, other := range sp.members {
		if other.roomID == m.roomID {
			return
		}
	}
	delete(sp.views, m.roomID)
}

// leaveGroup takes a session out of its group. The room keeps going for the
// others; the creator of a room made from a Jellyfin group ends it. Room
// membership is per profile, so it is only given up when no other session
// of the profile uses it: neither another bridge session nor the mediastorm
// app (the membership's client is then the app's, not ours).
func (s *Server) leaveGroup(ctx context.Context, m *syncMember, notify bool) {
	sp := s.syncPlayState()
	sp.dropMember(m)
	if m.roomID != "" && !s.profileStillIn(m) {
		var err error
		switch {
		case m.createdHere && s.membershipIsOurs(ctx, m):
			// (not while the creator still watches it in the app)
			err = s.deps.WatchRooms.End(ctx, m.roomID, m.p.Profile.ID)
		case m.creator:
			// The host's membership cannot be given up; the room's sweep
			// ends it if the host does not come back.
		case s.membershipIsOurs(ctx, m):
			err = s.deps.WatchRooms.Leave(ctx, m.roomID, m.p.Profile.ID)
		}
		if err != nil {
			log.Printf("[jellyfin-bridge] syncplay: leaving room %s: %v", m.roomID, err)
		}
	}
	if notify {
		s.pushGroup(m, "GroupLeft", m.groupID)
	}
}

// membershipIsOurs reports whether the profile's room membership is the
// bridge's to give up: not held by a mediastorm app that is still there.
func (s *Server) membershipIsOurs(ctx context.Context, m *syncMember) bool {
	room, err := s.deps.WatchRooms.Get(ctx, m.roomID, m.p.Profile.ID)
	return err == nil && !appHoldsMembership(room, m.p.Profile.ID)
}

// notMember reports the watch room service's "has not joined" error (the
// profile's membership was given up elsewhere, e.g. by the app).
func notMember(err error) bool {
	return err != nil && strings.Contains(err.Error(), "has not joined")
}

// heartbeat keeps the profile present in the room. A membership the
// mediastorm app claimed (its client ID) keeps that ID: the bridge does not
// take it over, so leaving the Jellyfin group later leaves it to the app.
// A membership given up elsewhere while this session still follows the
// room is taken back.
func (s *Server) heartbeat(ctx context.Context, m *syncMember, room *models.WatchRoom, buffering bool) {
	if appHoldsMembership(room, m.p.Profile.ID) {
		// The app keeps it present itself; it lapses (and the bridge takes
		// over) once the app stops.
		return
	}
	if err := s.deps.WatchRooms.Heartbeat(ctx, room.ID, m.p.Profile.ID, "jellyfin-"+m.p.Client.DeviceID, buffering); notMember(err) {
		_ = s.rejoin(ctx, m)
	}
}

// appHoldsMembership reports whether the profile's membership belongs to a
// mediastorm app that is still present (it heartbeats with its own client
// ID). An app that quit without leaving stops counting once its presence
// lapses.
func appHoldsMembership(room *models.WatchRoom, profileID string) bool {
	if room == nil {
		return false
	}
	for _, member := range room.Members {
		if member.ProfileID == profileID {
			return member.Present && member.ClientID != "" && !strings.HasPrefix(member.ClientID, "jellyfin-")
		}
	}
	return false
}

// rejoin restores a session's room membership after it was given up
// elsewhere while the session still follows the room.
func (s *Server) rejoin(ctx context.Context, m *syncMember) error {
	_, err := s.deps.WatchRooms.Join(ctx, m.roomID, m.p.Profile.ID, "jellyfin-"+m.p.Client.DeviceID, syncPlayCapabilities)
	return err
}

// profileStillIn reports whether another session of the same profile is in
// the room: the room knows profiles, not sessions.
func (s *Server) profileStillIn(m *syncMember) bool {
	for _, other := range s.syncPlayState().roomMembers(m.roomID) {
		if other.p.Profile.ID == m.p.Profile.ID {
			return true
		}
	}
	return false
}

// --- watcher -----------------------------------------------------------------

func (s *Server) syncWatch(sp *syncPlay) {
	t := time.NewTicker(syncPlayTick)
	defer t.Stop()
	for {
		select {
		case <-sp.stop:
			return
		case <-t.C:
			s.syncTick(context.Background())
		}
	}
}

// syncTick follows every room with bridge members: heartbeats, membership
// changes, state changes made elsewhere, and rooms that ended.
func (s *Server) syncTick(ctx context.Context) {
	// Bounded: a stalled database must not hold every SyncPlay request.
	ctx, cancel := context.WithTimeout(ctx, syncPlayTickBudget)
	defer cancel()
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	sp.mu.Lock()
	all := make([]*syncMember, 0, len(sp.members))
	for _, m := range sp.members {
		all = append(all, m)
	}
	sp.mu.Unlock()
	now := time.Now()
	rooms := map[string][]*syncMember{}
	for _, m := range all {
		// A session whose socket is gone for good leaves, so the room stops
		// counting it as present.
		if s.sockets.has(m.p.Profile.ID, m.p.Client.DeviceID, m.p.Session.Token) {
			m.noSocket = time.Time{}
		} else if m.noSocket.IsZero() {
			m.noSocket = now
		} else if now.Sub(m.noSocket) > syncPlaySocketGrace {
			// Its own time budget: the tick's may be spent by now.
			leaveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.leaveGroup(leaveCtx, m, false)
			cancel()
			continue
		}
		if m.roomID != "" {
			rooms[m.roomID] = append(rooms[m.roomID], m)
		}
	}
	for roomID, live := range rooms {
		if len(live) == 0 {
			continue
		}
		// Read the room as each member profile: a profile that lost access
		// answers "not found" while the room still runs for the others, so
		// only that profile's sessions leave.
		var (
			room      *models.WatchRoom
			err       error
			transient bool
			lost      = map[string]bool{}
			seen      = map[string]bool{}
		)
		for _, m := range live {
			pid := m.p.Profile.ID
			if seen[pid] {
				continue
			}
			seen[pid] = true
			r, gerr := s.deps.WatchRooms.Get(ctx, roomID, pid)
			switch {
			case gerr == nil:
				room = r
			case strings.Contains(gerr.Error(), "not found"):
				lost[pid] = true
			default:
				transient = true
			}
			if transient {
				break
			}
		}
		if transient {
			continue // try again next tick
		}
		if room == nil {
			err = errors.New("watch room not found")
		}
		if err == nil && len(lost) > 0 {
			kept := live[:0:0]
			for _, m := range live {
				if lost[m.p.Profile.ID] {
					s.syncPlayState().dropMember(m)
					s.pushGroup(m, "GroupLeft", m.groupID)
					continue
				}
				kept = append(kept, m)
			}
			live = kept
		}
		if err == nil && room.Status != models.WatchRoomStatusEnded {
			for _, m := range live {
				if now.Sub(m.lastBeat) >= syncPlayHeartbeat {
					m.lastBeat = now
					s.heartbeat(ctx, m, room, m.buffering)
				}
			}
		}
		if err != nil || room.Status == models.WatchRoomStatusEnded {
			for _, m := range live {
				s.syncPlayState().dropMember(m)
				s.pushGroup(m, "GroupLeft", m.groupID)
			}
			continue
		}
		s.syncRoomChanges(ctx, room, live)
	}
}

func (s *Server) syncRoomChanges(ctx context.Context, room *models.WatchRoom, members []*syncMember) {
	sp := s.syncPlayState()
	sp.mu.Lock()
	v := sp.views[room.ID]
	if v == nil {
		// Its last member left since this tick started.
		sp.mu.Unlock()
		return
	}
	changed := room.Revision > v.revision
	current := map[string]bool{}
	for _, n := range joinedNames(room) {
		current[n] = true
	}
	var joined, left []string
	for n := range current {
		if !v.names[n] {
			joined = append(joined, n)
		}
	}
	for n := range v.names {
		if !current[n] {
			left = append(left, n)
		}
	}
	v.names = current
	sp.mu.Unlock()
	for _, m := range members {
		for _, n := range joined {
			s.pushGroup(m, "UserJoined", n)
		}
		for _, n := range left {
			s.pushGroup(m, "UserLeft", n)
		}
	}
	if changed && room.WaitingForReady {
		// Started elsewhere: sessions that have the title loaded are ready.
		room = s.readyLoadedMembers(ctx, room)
	}
	if changed {
		reason := "Pause"
		if roomPlaying(room) {
			reason = "Unpause"
		}
		s.broadcastRoom(room, reason, false)
	}
}

func (s *Server) stopSyncPlay() {
	sp := s.syncPlayState()
	sp.mu.Lock()
	defer sp.mu.Unlock()
	select {
	case <-sp.stop:
	default:
		close(sp.stop)
	}
}

// --- endpoints ---------------------------------------------------------------

func (s *Server) handleUtcTime(w http.ResponseWriter, _ *http.Request) {
	received := time.Now().UTC()
	writeJSON(w, http.StatusOK, map[string]string{
		"RequestReceptionTime":     received.Format(time.RFC3339Nano),
		"ResponseTransmissionTime": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// syncRooms are the rooms the profile can join, as Jellyfin groups.
func (s *Server) syncRooms(ctx context.Context, p *principal) []models.WatchRoom {
	if !s.syncPlayEnabled(p) {
		return nil
	}
	rooms, err := s.deps.WatchRooms.Invitations(ctx, p.Profile.ID)
	if err != nil {
		return nil
	}
	out := rooms[:0]
	for _, room := range rooms {
		if room.Status == models.WatchRoomStatusEnded {
			continue
		}
		if _, ok := s.roomItemID(ctx, p, &room); ok {
			out = append(out, room)
		}
	}
	return out
}

func (s *Server) handleSyncPlayList(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	groups := []groupInfo{}
	for _, room := range s.syncRooms(r.Context(), p) {
		groups = append(groups, groupInfo{GroupID: compactID(room.ID), GroupName: roomGroupName(&room), State: groupState(&room),
			Participants: joinedNames(&room), LastUpdatedAt: room.AnchorUpdatedAt.UTC()})
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) handleSyncPlayGroup(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	want := compactID(pathVar(r, "id"))
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	if m := sp.member(p); m != nil && m.groupID == want {
		var room *models.WatchRoom
		if m.roomID != "" {
			room, _ = s.deps.WatchRooms.Get(r.Context(), m.roomID, p.Profile.ID)
		}
		writeJSON(w, http.StatusOK, s.groupInfoFor(m, room))
		return
	}
	for _, room := range s.syncRooms(r.Context(), p) {
		if compactID(room.ID) == want {
			writeJSON(w, http.StatusOK, groupInfo{GroupID: want, GroupName: roomGroupName(&room), State: groupState(&room), Participants: joinedNames(&room), LastUpdatedAt: room.AnchorUpdatedAt.UTC()})
			return
		}
	}
	writeError(w, http.StatusNotFound, "Group not found")
}

func decodeSyncBody(r *http.Request, out any) {
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(out)
	}
}

func (s *Server) handleSyncPlayJoin(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var in struct {
		GroupID string `json:"GroupId"`
	}
	decodeSyncBody(r, &in)
	groupID := compactID(in.GroupID)
	if !s.syncPlayEnabled(p) {
		writeError(w, http.StatusForbidden, "SyncPlay is not available")
		return
	}
	var target *models.WatchRoom
	for _, room := range s.syncRooms(r.Context(), p) {
		if compactID(room.ID) == groupID {
			target = &room
			break
		}
	}
	probe := &syncMember{p: p, groupID: groupID}
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	if target == nil {
		s.pushGroup(probe, "GroupDoesNotExist", groupID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if old := sp.member(p); old != nil {
		if old.roomID == target.ID {
			// Joining again (a reconnect, maybe after signing in anew):
			// take the current session and membership, resend the group
			// and its queue.
			old.p, old.loaded, old.seekTries = p, false, 0
			room, err := s.deps.WatchRooms.Join(r.Context(), target.ID, p.Profile.ID, "jellyfin-"+p.Client.DeviceID, syncPlayCapabilities)
			if err != nil {
				s.leaveGroup(r.Context(), old, true)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			s.pushGroup(old, "GroupJoined", s.groupInfoFor(old, room))
			s.pushQueue(old, room)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.leaveGroup(r.Context(), old, true)
	}
	itemID, ok := s.roomItemID(r.Context(), p, target)
	if !ok {
		s.pushGroup(probe, "LibraryAccessDenied", groupID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	room, err := s.deps.WatchRooms.Join(r.Context(), target.ID, p.Profile.ID, "jellyfin-"+p.Client.DeviceID, syncPlayCapabilities)
	if err != nil {
		s.pushGroup(probe, "GroupDoesNotExist", groupID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	m := &syncMember{p: p, groupID: groupID, roomID: room.ID, groupName: roomGroupName(room), itemID: itemID,
		playlist: deriveID("syncplay-entry", room.ID), creator: room.CreatorProfileID == p.Profile.ID, createdHere: roomFromJellyfin(room, p.Profile.ID), joinedAt: time.Now().UTC(), lastBeat: time.Now()}
	s.addMember(m, room)
	s.pushGroup(m, "GroupJoined", s.groupInfoFor(m, room))
	for _, other := range sp.roomMembers(room.ID) {
		if other != m {
			s.pushGroup(other, "UserJoined", p.Profile.Name)
		}
	}
	s.pushQueue(m, room)
	s.pushState(m, room, "Play")
	w.WriteHeader(http.StatusNoContent)
}

// handleSyncPlayNew starts a group whose room is created with its first
// queue (a room needs its title up front).
func (s *Server) handleSyncPlayNew(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var in struct {
		GroupName string `json:"GroupName"`
	}
	decodeSyncBody(r, &in)
	if !s.syncPlayEnabled(p) {
		writeError(w, http.StatusForbidden, "SyncPlay is not available")
		return
	}
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	if old := sp.member(p); old != nil {
		s.leaveGroup(r.Context(), old, true)
	}
	name := strings.TrimSpace(in.GroupName)
	if name == "" {
		name = p.Profile.Name
	}
	m := &syncMember{p: p, groupID: randomHex(16), groupName: name, creator: true, joinedAt: time.Now().UTC(), lastBeat: time.Now()}
	s.addMember(m, nil)
	info := s.groupInfoFor(m, nil)
	s.pushGroup(m, "GroupJoined", info)
	writeJSON(w, http.StatusOK, info)
}

// handleSyncPlaySetNewQueue creates the room for a new group from the item
// being played; a room's title is fixed afterwards.
func (s *Server) handleSyncPlaySetNewQueue(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var in struct {
		PlayingQueue        []string `json:"PlayingQueue"`
		PlayingItemPosition int      `json:"PlayingItemPosition"`
		StartPositionTicks  int64    `json:"StartPositionTicks"`
	}
	decodeSyncBody(r, &in)
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	m := sp.member(p)
	if m == nil {
		s.pushGroup(&syncMember{p: p}, "NotInGroup", "")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if m.roomID != "" || len(in.PlayingQueue) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	idx := in.PlayingItemPosition
	if idx < 0 || idx >= len(in.PlayingQueue) {
		idx = 0
	}
	create, ok := s.roomCreateFor(r.Context(), p, in.PlayingQueue[idx])
	if !ok {
		s.pushGroup(m, "LibraryAccessDenied", m.groupID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// A group made in Jellyfin is open to the creator's household; other
	// households are invited from the mediastorm app.
	for _, profile := range s.deps.Users.ListAll() {
		// Kids profiles are not invited: the title was not chosen for them.
		if profile.ID != p.Profile.ID && profile.AccountID == p.Profile.AccountID && !profile.IsKidsProfile {
			create.InviteeProfileIDs = append(create.InviteeProfileIDs, profile.ID)
		}
	}
	create.ClientID, create.Capabilities = "jellyfin-"+p.Client.DeviceID, syncPlayCapabilities
	room, err := s.deps.WatchRooms.Create(r.Context(), p.Session.AccountID, p.Profile.ID, create)
	if err != nil {
		log.Printf("[jellyfin-bridge] syncplay: creating room: %v", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Playing a queue starts the room (as in Jellyfin): it waits for its
	// members to load, and the creator's Ready releases it.
	if updated, err := s.deps.WatchRooms.UpdateState(r.Context(), room.ID, p.Profile.ID, models.WatchRoomStateUpdate{Status: models.WatchRoomStatusPlaying, Position: ticksToSeconds(in.StartPositionTicks)}); err == nil {
		room = updated
	}
	sp.mu.Lock()
	m.roomID, m.itemID, m.playlist, m.createdHere = room.ID, in.PlayingQueue[idx], deriveID("syncplay-entry", room.ID), true
	sp.mu.Unlock()
	s.addMember(m, room)
	s.pushQueue(m, room)
	s.pushState(m, room, "Play")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSyncPlayLeave(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	if m := sp.member(p); m != nil {
		s.leaveGroup(r.Context(), m, true)
	} else {
		s.pushGroup(&syncMember{p: p}, "NotInGroup", "")
	}
	w.WriteHeader(http.StatusNoContent)
}

// memberRoom is the session's member entry and current room (the caller
// holds sp.op).
func (s *Server) memberRoom(w http.ResponseWriter, r *http.Request) (*syncMember, *models.WatchRoom, bool) {
	p := principalFrom(r)
	m := s.syncPlayState().member(p)
	if m == nil || m.roomID == "" {
		if m == nil {
			s.pushGroup(&syncMember{p: p}, "NotInGroup", "")
		}
		w.WriteHeader(http.StatusNoContent)
		return nil, nil, false
	}
	room, err := s.deps.WatchRooms.Get(r.Context(), m.roomID, p.Profile.ID)
	if err != nil && !strings.Contains(err.Error(), "not found") {
		// Transient (database, cancelled request): the member stays.
		writeError(w, http.StatusServiceUnavailable, "Group unavailable, try again")
		return nil, nil, false
	}
	if err != nil || room.Status == models.WatchRoomStatusEnded {
		s.syncPlayState().dropMember(m)
		s.pushGroup(m, "GroupLeft", m.groupID)
		w.WriteHeader(http.StatusNoContent)
		return nil, nil, false
	}
	return m, room, true
}

// updateRoom applies a playback change and pushes the result to everyone;
// a revision conflict (someone else changed it first) pushes that instead.
func (s *Server) updateRoom(ctx context.Context, m *syncMember, room *models.WatchRoom, status string, position float64, reason string, seek bool) {
	rev := room.Revision
	update := models.WatchRoomStateUpdate{Status: status, Position: position, ExpectedRevision: &rev}
	updated, err := s.deps.WatchRooms.UpdateState(ctx, room.ID, m.p.Profile.ID, update)
	if notMember(err) && s.rejoin(ctx, m) == nil {
		updated, err = s.deps.WatchRooms.UpdateState(ctx, room.ID, m.p.Profile.ID, update)
	}
	if err != nil {
		if fresh, gerr := s.deps.WatchRooms.Get(ctx, room.ID, m.p.Profile.ID); gerr == nil {
			s.broadcastRoom(fresh, reason, false)
		}
		return
	}
	if updated.WaitingForReady {
		updated = s.readyLoadedMembers(ctx, updated)
	}
	s.broadcastRoom(updated, reason, seek)
}

// readyLoadedMembers reports every bridge session that has its playback
// loaded as ready when a room starts, so the room waits only for those still
// loading (the mediastorm apps report their own).
func (s *Server) readyLoadedMembers(ctx context.Context, room *models.WatchRoom) *models.WatchRoom {
	for _, other := range s.syncPlayState().roomMembers(room.ID) {
		if other.loaded && !other.buffering {
			if r, err := s.deps.WatchRooms.SetReady(ctx, room.ID, other.p.Profile.ID, true); err == nil {
				room = r
			}
		}
	}
	return room
}

func (s *Server) handleSyncPlayPause(w http.ResponseWriter, r *http.Request) {
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	if m, room, ok := s.memberRoom(w, r); ok {
		s.updateRoom(r.Context(), m, room, models.WatchRoomStatusPaused, roomPosition(room, time.Now()), "Pause", false)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleSyncPlayUnpause(w http.ResponseWriter, r *http.Request) {
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	if m, room, ok := s.memberRoom(w, r); ok {
		if room.Status == models.WatchRoomStatusLobby && !m.creator {
			// The host starts a room; until then this session holds.
			s.pushCommand(m, room)
		} else {
			s.updateRoom(r.Context(), m, room, models.WatchRoomStatusPlaying, roomPosition(room, time.Now()), "Unpause", false)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleSyncPlaySeek(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PositionTicks int64 `json:"PositionTicks"`
	}
	decodeSyncBody(r, &in)
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	if m, room, ok := s.memberRoom(w, r); ok {
		status := models.WatchRoomStatusPaused
		if room.Status == models.WatchRoomStatusPlaying {
			status = models.WatchRoomStatusPlaying
		}
		if room.Status == models.WatchRoomStatusLobby {
			// A seek does not start a room still in its lobby (that would
			// skip waiting for everyone to load).
			s.pushCommand(m, room)
		} else {
			s.updateRoom(r.Context(), m, room, status, ticksToSeconds(in.PositionTicks), "Seek", true)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleSyncPlayStop stops this session's playback only: the room keeps
// playing for the others.
func (s *Server) handleSyncPlayStop(w http.ResponseWriter, r *http.Request) {
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	if m, _, ok := s.memberRoom(w, r); ok {
		now := time.Now().UTC()
		m.loaded = false
		s.sockets.sendToDevice(m.p.Profile.ID, m.p.Client.DeviceID, m.p.Session.Token, "SyncPlayCommand", syncCommand{GroupID: m.groupID, PlaylistItemID: m.playlist, When: now, Command: "Stop", EmittedAt: now})
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleSyncPlayBuffering(w http.ResponseWriter, r *http.Request) {
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	if m, room, ok := s.memberRoom(w, r); ok {
		m.buffering = true
		m.lastBeat = time.Now()
		s.heartbeat(r.Context(), m, room, true)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleSyncPlayReady: the session has its playback loaded (or finished
// seeking or buffering). A creator's ready starts a room still in its lobby;
// a room waiting for everyone gets this member's ready; then the session
// gets the room's current command.
func (s *Server) handleSyncPlayReady(w http.ResponseWriter, r *http.Request) {
	var in struct {
		When          *time.Time `json:"When"`
		PositionTicks int64      `json:"PositionTicks"`
		IsPlaying     bool       `json:"IsPlaying"`
	}
	decodeSyncBody(r, &in)
	sp := s.syncPlayState()
	sp.op.Lock()
	defer sp.op.Unlock()
	m, room, ok := s.memberRoom(w, r)
	if !ok {
		return
	}
	// Where the member is now: its report, moved on if it was playing.
	clientPos := ticksToSeconds(in.PositionTicks)
	if in.IsPlaying && in.When != nil {
		if d := time.Since(*in.When).Seconds(); d > 0 && d < 10 {
			clientPos += d
		}
	}
	m.loaded, m.buffering, m.lastBeat = true, false, time.Now()
	ctx := r.Context()
	s.heartbeat(ctx, m, room, false)
	switch {
	case room.Status == models.WatchRoomStatusLobby && m.creator:
		s.updateRoom(ctx, m, room, models.WatchRoomStatusPlaying, roomPosition(room, time.Now()), "Ready", false)
	case room.WaitingForReady:
		if updated, err := s.deps.WatchRooms.SetReady(ctx, room.ID, m.p.Profile.ID, true); err == nil {
			if !updated.WaitingForReady {
				s.broadcastRoom(updated, "Ready", false)
			} else {
				s.pushCommand(m, updated)
			}
		}
	default:
		s.alignMember(m, room, clientPos, in.IsPlaying)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSyncPlayIgnored accepts requests a single-title room has no use for
// (queue editing, next/previous, repeat, shuffle, ping, ignore-wait).
func (s *Server) handleSyncPlayIgnored(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
