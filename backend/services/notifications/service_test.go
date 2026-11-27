package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"novastream/models"
)

type memoryRepo struct {
	mu           sync.Mutex
	channels     map[string]models.NotificationChannel
	observations map[string]models.NotificationObservation
	progress     map[string]models.NotificationProgressMessage
}

func newMemoryRepo() *memoryRepo {
	return &memoryRepo{
		channels:     make(map[string]models.NotificationChannel),
		observations: make(map[string]models.NotificationObservation),
		progress:     make(map[string]models.NotificationProgressMessage),
	}
}

func (r *memoryRepo) GetChannel(_ context.Context, id string) (*models.NotificationChannel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	channel, ok := r.channels[id]
	if !ok {
		return nil, nil
	}
	return &channel, nil
}

func (r *memoryRepo) ListChannels(_ context.Context, profileID string) ([]models.NotificationChannel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var channels []models.NotificationChannel
	for _, channel := range r.channels {
		if channel.ProfileID == profileID {
			channels = append(channels, channel)
		}
	}
	return channels, nil
}

func (r *memoryRepo) ListAllChannels(_ context.Context) ([]models.NotificationChannel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	channels := make([]models.NotificationChannel, 0, len(r.channels))
	for _, channel := range r.channels {
		channels = append(channels, channel)
	}
	return channels, nil
}

func (r *memoryRepo) CreateChannel(_ context.Context, channel *models.NotificationChannel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[channel.ID] = *channel
	return nil
}

func (r *memoryRepo) UpdateChannel(_ context.Context, channel *models.NotificationChannel) error {
	return r.CreateChannel(context.Background(), channel)
}

func (r *memoryRepo) DeleteChannel(_ context.Context, profileID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if channel, ok := r.channels[id]; ok && channel.ProfileID == profileID {
		delete(r.channels, id)
	}
	return nil
}

func observationID(profileID, itemKey string) string { return profileID + "\x00" + itemKey }

func (r *memoryRepo) GetObservation(_ context.Context, profileID, itemKey string) (*models.NotificationObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	observation, ok := r.observations[observationID(profileID, itemKey)]
	if !ok {
		return nil, nil
	}
	return &observation, nil
}

func (r *memoryRepo) ListObservations(_ context.Context, profileID string) ([]models.NotificationObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var observations []models.NotificationObservation
	for _, observation := range r.observations {
		if observation.ProfileID == profileID {
			observations = append(observations, observation)
		}
	}
	return observations, nil
}

func (r *memoryRepo) ListAllObservations(_ context.Context) ([]models.NotificationObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	observations := make([]models.NotificationObservation, 0, len(r.observations))
	for _, observation := range r.observations {
		observations = append(observations, observation)
	}
	return observations, nil
}

func (r *memoryRepo) UpsertObservation(_ context.Context, observation *models.NotificationObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations[observationID(observation.ProfileID, observation.ItemKey)] = *observation
	return nil
}

func progressMessageID(channelID, playbackKey string) string {
	return channelID + "\x00" + playbackKey
}

func (r *memoryRepo) GetProgressMessage(_ context.Context, channelID, playbackKey string) (*models.NotificationProgressMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	message, ok := r.progress[progressMessageID(channelID, playbackKey)]
	if !ok {
		return nil, nil
	}
	return &message, nil
}

func (r *memoryRepo) ListProgressMessages(_ context.Context) ([]models.NotificationProgressMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	messages := make([]models.NotificationProgressMessage, 0, len(r.progress))
	for _, message := range r.progress {
		messages = append(messages, message)
	}
	return messages, nil
}

func (r *memoryRepo) ListProgressMessagesByPlayback(_ context.Context, profileID, playbackKey string) ([]models.NotificationProgressMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var messages []models.NotificationProgressMessage
	for _, message := range r.progress {
		if message.ProfileID == profileID && message.PlaybackKey == playbackKey {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func (r *memoryRepo) UpsertProgressMessage(_ context.Context, message *models.NotificationProgressMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress[progressMessageID(message.ChannelID, message.PlaybackKey)] = *message
	return nil
}

func (r *memoryRepo) TouchProgressMessages(_ context.Context, profileID, playbackKey string, updatedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, message := range r.progress {
		if message.ProfileID == profileID && message.PlaybackKey == playbackKey {
			message.UpdatedAt = updatedAt
			r.progress[key] = message
		}
	}
	return nil
}

func (r *memoryRepo) DeleteProgressMessage(_ context.Context, channelID, playbackKey string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.progress, progressMessageID(channelID, playbackKey))
	return nil
}

func TestFormatRendersSupportedSections(t *testing.T) {
	title, body := Format(models.NotificationChannel{
		TitleTemplate: "{{eventLabel}}: {{title}}",
		BodyTemplate:  "{{mediaLabel}}{{progressLabel}}{{releaseLabel}}",
	}, models.NotificationEvent{
		Type:          models.NotificationEventWatchResumed,
		Title:         "Pilot",
		MediaType:     "episode",
		SeriesTitle:   "Example Show",
		EpisodeTitle:  "Pilot",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		Percent:       42,
	})
	if title != "Resumed: Example Show" {
		t.Fatalf("title = %q", title)
	}
	if body != "Episode · S01E02 · Pilot · 42%" {
		t.Fatalf("body = %q", body)
	}
}

func TestFormatOmitsProgressForWatchedEvent(t *testing.T) {
	title, body := Format(models.NotificationChannel{
		TitleTemplate: "{{eventLabel}}: {{title}} {{percent}}",
		BodyTemplate:  "{{mediaLabel}}{{progressLabel}}",
	}, models.NotificationEvent{
		Type:      models.NotificationEventWatchWatched,
		Title:     "Spirit: Stallion of the Cimarron",
		MediaType: "movie",
		Percent:   90,
	})
	if title != "Watched: Spirit: Stallion of the Cimarron" {
		t.Fatalf("title = %q", title)
	}
	if body != "Movie" {
		t.Fatalf("body = %q", body)
	}
}

func TestFormatIncludesZeroPercentForProgressEvent(t *testing.T) {
	title, body := Format(models.NotificationChannel{
		TitleTemplate: "{{eventLabel}}: {{title}}",
		BodyTemplate:  "{{mediaLabel}}{{progressLabel}}",
	}, models.NotificationEvent{
		Type:      models.NotificationEventWatchProgress,
		Title:     "Movie",
		MediaType: "movie",
		Percent:   0,
	})
	if title != "Watching: Movie" {
		t.Fatalf("title = %q", title)
	}
	if body != "Movie · 0%" {
		t.Fatalf("body = %q", body)
	}
}

func TestProgressBarKeepsStartingSegmentFilled(t *testing.T) {
	tests := []struct {
		name    string
		percent float64
		filled  int
	}{
		{name: "negative", percent: -1, filled: 1},
		{name: "zero", percent: 0, filled: 1},
		{name: "below first rounded segment", percent: 2, filled: 1},
		{name: "next segment", percent: 8, filled: 2},
		{name: "complete", percent: 100, filled: 20},
		{name: "above complete", percent: 101, filled: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bar := progressBar(tt.percent)
			if got := strings.Count(bar, "▰"); got != tt.filled {
				t.Fatalf("filled segments = %d, want %d in %q", got, tt.filled, bar)
			}
			if got := utf8.RuneCountInString(bar); got != 20 {
				t.Fatalf("bar width = %d, want 20 in %q", got, bar)
			}
		})
	}
}

func TestFormatCapitalizesReleaseTypes(t *testing.T) {
	tests := []struct {
		releaseType string
		want        string
	}{
		{releaseType: "digital", want: "Digital"},
		{releaseType: "physical", want: "Physical"},
		{releaseType: "theatrical", want: "Theatrical"},
		{releaseType: "theatricalLimited", want: "Limited Theatrical"},
		{releaseType: "premiere", want: "Premiere"},
		{releaseType: "tv", want: "TV"},
	}
	for _, tt := range tests {
		t.Run(tt.releaseType, func(t *testing.T) {
			_, body := Format(models.NotificationChannel{
				BodyTemplate: "{{releaseType}}{{releaseLabel}}",
			}, models.NotificationEvent{
				Type:        models.NotificationEventRelease,
				ReleaseType: tt.releaseType,
				ReleaseDate: "2026-07-25",
			})
			want := tt.want + " · " + tt.want + " · 2026-07-25"
			if body != want {
				t.Fatalf("body = %q, want %q", body, want)
			}
		})
	}
}

func TestNotificationReleaseArtworkUsesOrientationByMediaType(t *testing.T) {
	item := models.CalendarItem{
		PosterURL:       "https://image.example/poster.jpg",
		TextPosterURL:   "https://image.example/text-poster.jpg",
		BackdropURL:     "https://image.example/backdrop.jpg",
		TextBackdropURL: "https://image.example/text-backdrop.jpg",
		BackdropURLs:    []string{"https://image.example/alternate-backdrop.jpg"},
	}

	item.MediaType = "movie"
	if got := notificationReleaseArtwork(item); got != item.TextPosterURL {
		t.Fatalf("movie artwork = %q, want portrait %q", got, item.TextPosterURL)
	}

	item.MediaType = "episode"
	if got := notificationReleaseArtwork(item); got != item.TextBackdropURL {
		t.Fatalf("episode artwork = %q, want landscape %q", got, item.TextBackdropURL)
	}

	item.MediaType = "series"
	item.TextBackdropURL = ""
	if got := notificationReleaseArtwork(item); got != item.BackdropURL {
		t.Fatalf("series artwork = %q, want landscape fallback %q", got, item.BackdropURL)
	}
}

func TestNotificationReleaseArtworkFallsBackAcrossOrientations(t *testing.T) {
	movie := models.CalendarItem{
		MediaType:   "movie",
		BackdropURL: "https://image.example/backdrop.jpg",
	}
	if got := notificationReleaseArtwork(movie); got != movie.BackdropURL {
		t.Fatalf("movie fallback artwork = %q, want %q", got, movie.BackdropURL)
	}

	episode := models.CalendarItem{
		MediaType: "episode",
		PosterURL: "https://image.example/poster.jpg",
	}
	if got := notificationReleaseArtwork(episode); got != episode.PosterURL {
		t.Fatalf("episode fallback artwork = %q, want %q", got, episode.PosterURL)
	}
}

func TestSaveAndListChannelPreservesAndMasksWebhookURL(t *testing.T) {
	repo := newMemoryRepo()
	service := New(repo)
	defer service.Close()

	saved, err := service.SaveChannel(context.Background(), models.NotificationChannel{
		ProfileID: "profile", Name: "Automation", Type: models.NotificationChannelWebhook,
		URL: "https://example.com/hook/secret", Enabled: true,
		Events: []string{models.NotificationEventWatchWatched},
	})
	if err != nil {
		t.Fatalf("save channel: %v", err)
	}
	if saved.URL != "" || !saved.URLConfigured {
		t.Fatalf("saved URL exposure = %q configured=%t", saved.URL, saved.URLConfigured)
	}

	saved.Name = "Renamed"
	saved.URL = ""
	if _, err := service.SaveChannel(context.Background(), saved); err != nil {
		t.Fatalf("update channel: %v", err)
	}
	stored := repo.channels[saved.ID]
	if stored.URL != "https://example.com/hook/secret" {
		t.Fatalf("stored URL = %q", stored.URL)
	}

	listed, err := service.ListChannels(context.Background(), "profile")
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	if len(listed) != 1 || listed[0].URL != "" || !listed[0].URLConfigured {
		t.Fatalf("listed channel = %#v", listed)
	}
}

func TestSaveReleaseChannelDefaultsToDigitalAndPhysical(t *testing.T) {
	repo := newMemoryRepo()
	service := New(repo)
	defer service.Close()

	saved, err := service.SaveChannel(context.Background(), models.NotificationChannel{
		ProfileID: "profile", Name: "Releases", Type: models.NotificationChannelWebhook,
		URL: "https://example.com/hook", Enabled: true,
		Events:          []string{models.NotificationEventRelease},
		NotifyWatchlist: true,
	})
	if err != nil {
		t.Fatalf("save channel: %v", err)
	}
	if got, want := strings.Join(saved.ReleaseTypes, ","), "digital,physical"; got != want {
		t.Fatalf("release types = %q, want %q", got, want)
	}
}

func TestSaveReleaseChannelRequiresReleaseTypeWhenExplicitlyEmpty(t *testing.T) {
	service := New(newMemoryRepo())
	defer service.Close()

	_, err := service.SaveChannel(context.Background(), models.NotificationChannel{
		ProfileID: "profile", Name: "Releases", Type: models.NotificationChannelWebhook,
		URL:             "https://example.com/hook",
		Events:          []string{models.NotificationEventRelease},
		NotifyWatchlist: true,
		ReleaseTypes:    []string{},
	})
	if err == nil {
		t.Fatal("expected empty release types to be rejected")
	}
}

func TestChannelAcceptsOnlySelectedReleaseTypes(t *testing.T) {
	channel := models.NotificationChannel{
		NotifyWatchlist: true,
		ReleaseTypes:    []string{"digital", "physical"},
	}
	event := models.NotificationEvent{
		Type:   models.NotificationEventRelease,
		Source: "watchlist",
	}
	for _, releaseType := range []string{"digital", "physical"} {
		event.ReleaseType = releaseType
		if !channelAcceptsRelease(channel, event) {
			t.Fatalf("expected %q to be accepted", releaseType)
		}
	}
	for _, releaseType := range []string{"tv", "theatrical", "availability", ""} {
		event.ReleaseType = releaseType
		if channelAcceptsRelease(channel, event) {
			t.Fatalf("expected %q to be rejected", releaseType)
		}
	}
}

func TestDiscordDeliveryEmbedsPublicPoster(t *testing.T) {
	var received struct {
		Embeds []struct {
			Thumbnail struct {
				URL string `json:"url"`
			} `json:"thumbnail"`
		} `json:"embeds"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode Discord payload: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	service := New(newMemoryRepo())
	defer service.Close()
	err := service.deliver(context.Background(), models.NotificationChannel{
		Type:          models.NotificationChannelDiscord,
		URL:           server.URL,
		IncludePoster: true,
		TitleTemplate: defaultTitleTemplate,
		BodyTemplate:  defaultBodyTemplate,
	}, models.NotificationEvent{
		Type:       models.NotificationEventWatchStarted,
		Title:      "Example",
		MediaType:  "movie",
		PosterURL:  "https://image.tmdb.org/t/p/w500/example.jpg",
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("deliver Discord notification: %v", err)
	}
	if len(received.Embeds) != 1 {
		t.Fatalf("Discord embeds = %d, want 1", len(received.Embeds))
	}
	if got := received.Embeds[0].Thumbnail.URL; got != "https://image.tmdb.org/t/p/w500/example.jpg" {
		t.Fatalf("Discord thumbnail URL = %q", got)
	}
}

func TestReleaseDeliveryDeduplicatesOverlappingSources(t *testing.T) {
	received := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true, Events: []string{models.NotificationEventRelease},
		NotifyWatchlist: true, NotifyTrending: true, TrendingLimit: 20,
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	base := models.NotificationEvent{
		ID: "watchlist", Type: models.NotificationEventRelease, ProfileID: "profile",
		Title: "Movie", MediaType: "movie", ReleaseType: "digital", ReleaseDate: "2026-07-23",
		ExternalIDs: map[string]string{"tvdb": "2"}, Source: "watchlist", OccurredAt: time.Now().UTC(),
	}
	service.Notify(base)
	base.ID = "trending"
	base.Source = "top-trending"
	base.SourceRank = 1
	base.ExternalIDs = map[string]string{"tmdb": "1", "tvdb": "2", "imdb": "tt0000001"}
	service.Notify(base)
	base.ID = "trending-tmdb-only"
	base.ExternalIDs = map[string]string{"tmdb": "1"}
	service.Notify(base)
	base.ID = "trending-availability"
	base.ReleaseType = "availability"
	base.ReleaseDate = ""
	service.Notify(base)

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for release event")
	}
	select {
	case duplicate := <-received:
		t.Fatalf("received duplicate event %q", duplicate)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestSaveChannelRequiresSingleNotificationType(t *testing.T) {
	repo := newMemoryRepo()
	service := New(repo)
	defer service.Close()

	_, err := service.SaveChannel(context.Background(), models.NotificationChannel{
		ProfileID: "profile",
		Name:      "Mixed",
		Type:      models.NotificationChannelWebhook,
		URL:       "https://example.com/hook",
		Events: []string{
			models.NotificationEventWatchStarted,
			models.NotificationEventRelease,
		},
		NotifyWatchlist: true,
	})
	if err == nil {
		t.Fatal("expected mixed notification types to be rejected")
	}
}

func TestSaveChannelKeepsSystemOperationsSeparateFromMediaEvents(t *testing.T) {
	service := New(newMemoryRepo())
	defer service.Close()

	_, err := service.SaveChannel(context.Background(), models.NotificationChannel{
		ProfileID: "profile",
		Name:      "Mixed",
		Type:      models.NotificationChannelWebhook,
		URL:       "https://example.com/hook",
		Events: []string{
			models.NotificationEventWatchStarted,
			models.NotificationEventSystemStartup,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "system operations") {
		t.Fatalf("SaveChannel error = %v, want system operations validation error", err)
	}

	saved, err := service.SaveChannel(context.Background(), models.NotificationChannel{
		ProfileID: "profile",
		Name:      "Lifecycle",
		Type:      models.NotificationChannelWebhook,
		URL:       "https://example.com/hook",
		Events: []string{
			models.NotificationEventSystemStartup,
			models.NotificationEventSystemShutdown,
		},
	})
	if err != nil {
		t.Fatalf("save system channel: %v", err)
	}
	if got := strings.Join(saved.Events, ","); got != "system.shutdown,system.startup" {
		t.Fatalf("system events = %q", got)
	}
}

func TestNotifySystemSynchronouslyDeliversOnlySubscribedEvent(t *testing.T) {
	type receivedPayload struct {
		Event string                   `json:"event"`
		Data  models.NotificationEvent `json:"data"`
	}
	received := make(chan receivedPayload, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload receivedPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode system notification: %v", err)
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["startup"] = models.NotificationChannel{
		ID: "startup", ProfileID: "profile-a", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true, Events: []string{models.NotificationEventSystemStartup},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	repo.channels["shutdown"] = models.NotificationChannel{
		ID: "shutdown", ProfileID: "profile-b", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true, Events: []string{models.NotificationEventSystemShutdown},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	repo.channels["disabled"] = models.NotificationChannel{
		ID: "disabled", ProfileID: "profile-c", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: false, Events: []string{models.NotificationEventSystemStartup},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	if err := service.NotifySystem(context.Background(), models.NotificationEventSystemStartup); err != nil {
		t.Fatalf("notify startup: %v", err)
	}
	payload := waitForNotificationRequest(t, received)
	if payload.Event != models.NotificationEventSystemStartup ||
		payload.Data.ProfileID != "profile-a" ||
		payload.Data.Title != "mediastorm" ||
		payload.Data.MediaType != "system" {
		t.Fatalf("startup payload = %#v", payload)
	}
	select {
	case extra := <-received:
		t.Fatalf("unexpected system notification = %#v", extra)
	default:
	}
}

func TestReleaseIdentityUsesCanonicalExternalID(t *testing.T) {
	watchlist := models.NotificationEvent{
		Title:       "Movie",
		Year:        2026,
		MediaType:   "movie",
		ReleaseType: "digital",
		ReleaseDate: "2026-07-23",
		ExternalIDs: map[string]string{"tmdb": "1"},
	}
	trending := watchlist
	trending.ExternalIDs = map[string]string{
		"TMDB_ID": "1",
		"imdb":    "tt0000001",
	}
	if got, want := releaseEventIdentity(trending), releaseEventIdentity(watchlist); got != want {
		t.Fatalf("release identities differ: got %q, want %q", got, want)
	}
}

func TestPlaybackNotificationsAreEdgeTriggered(t *testing.T) {
	received := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true,
		Events: []string{
			models.NotificationEventWatchStarted,
			models.NotificationEventWatchResumed,
			models.NotificationEventWatchWatched,
		},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	update := models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:1", MovieName: "Movie", Duration: 100,
	}
	service.HandlePlaybackUpdate("profile", update, 1)
	update.IsPaused = true
	service.HandlePlaybackUpdate("profile", update, 10)
	update.IsPaused = false
	service.HandlePlaybackUpdate("profile", update, 11)
	service.HandlePlaybackUpdate("profile", update, 95)

	want := []string{
		models.NotificationEventWatchStarted,
		models.NotificationEventWatchResumed,
		models.NotificationEventWatchWatched,
	}
	for i, expected := range want {
		select {
		case actual := <-received:
			if actual != expected {
				t.Fatalf("event %d = %q, want %q", i, actual, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %q", expected)
		}
	}
}

func TestPlaybackNotificationsExcludeLiveTVAndRecordings(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelDiscord,
		URL: server.URL + "/api/webhooks/1/token", Enabled: true,
		Events: []string{
			models.NotificationEventWatchStarted,
			models.NotificationEventWatchProgress,
			models.NotificationEventWatchWatched,
		},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	for _, mediaType := range []string{"live", "Live", " live ", "livetv", "live-tv", "channel", "channels"} {
		service.HandlePlaybackUpdate("profile", models.PlaybackProgressUpdate{
			MediaType: mediaType,
			ItemID:    "live-channel",
			MovieName: "Live Channel",
			Duration:  100,
		}, 50)
	}
	service.HandlePlaybackUpdate("profile", models.PlaybackProgressUpdate{
		MediaType:  "movie",
		ItemID:     "recording-item",
		SourcePath: "recording:recording-id/recording.ts",
		MovieName:  "Recorded Programme",
		Duration:   100,
	}, 50)

	select {
	case event := <-received:
		t.Fatalf("excluded playback emitted %q notification", event)
	case <-time.After(100 * time.Millisecond):
	}

	service.sessionMu.Lock()
	sessionCount := len(service.sessions)
	service.sessionMu.Unlock()
	if sessionCount != 0 {
		t.Fatalf("excluded playback created %d notification sessions", sessionCount)
	}
	if len(repo.progress) != 0 {
		t.Fatalf("excluded playback persisted %d progress notifications", len(repo.progress))
	}
}

func TestPlaybackNotificationPrefersOrientationSelectedArtwork(t *testing.T) {
	received := make(chan models.NotificationEvent, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Data models.NotificationEvent `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Data
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true, IncludePoster: true,
		Events: []string{models.NotificationEventWatchStarted},
	}
	service := New(repo)
	defer service.Close()

	service.HandlePlaybackUpdate("profile", models.PlaybackProgressUpdate{
		MediaType:            "episode",
		ItemID:               "episode:1",
		SeriesName:           "Show",
		PosterURL:            "https://image.example/poster.jpg",
		NotificationImageURL: "https://image.example/backdrop.jpg",
	}, 1)

	select {
	case event := <-received:
		if event.PosterURL != "https://image.example/backdrop.jpg" {
			t.Fatalf("notification artwork = %q, want orientation-selected backdrop", event.PosterURL)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for playback notification")
	}
}

func TestPlaybackNotificationsWaitForActivePlaybackBeforeStarting(t *testing.T) {
	received := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true,
		Events:        []string{models.NotificationEventWatchStarted, models.NotificationEventWatchResumed},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	update := models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:1", MovieName: "Movie", IsPaused: true,
	}
	service.HandlePlaybackUpdate("profile", update, 1)
	select {
	case event := <-received:
		t.Fatalf("paused initial heartbeat emitted %q", event)
	case <-time.After(50 * time.Millisecond):
	}

	update.IsPaused = false
	service.HandlePlaybackUpdate("profile", update, 2)
	select {
	case event := <-received:
		if event != models.NotificationEventWatchStarted {
			t.Fatalf("first active heartbeat emitted %q", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for started event")
	}
	select {
	case event := <-received:
		t.Fatalf("first active heartbeat also emitted %q", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPlaybackNotificationsTrackActiveStreamSessionsIndependently(t *testing.T) {
	service := New(newMemoryRepo())
	defer service.Close()

	update := models.PlaybackProgressUpdate{
		MediaType:         "movie",
		ItemID:            "tmdb:1",
		MovieName:         "Movie",
		PlaybackSessionID: "hls:first",
	}
	service.HandlePlaybackUpdate("profile", update, 1)
	update.PlaybackSessionID = "hls:second"
	service.HandlePlaybackUpdate("profile", update, 1)

	service.sessionMu.Lock()
	sessionCount := len(service.sessions)
	service.sessionMu.Unlock()
	if sessionCount != 2 {
		t.Fatalf("playback session count = %d, want 2", sessionCount)
	}
}

func TestDiscordProgressNotificationEditsThenCompletesOneMessage(t *testing.T) {
	type request struct {
		method string
		path   string
		query  string
		title  string
		body   string
	}
	received := make(chan request, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Embeds []struct {
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"embeds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		item := request{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
		if len(payload.Embeds) > 0 {
			item.title = payload.Embeds[0].Title
			item.body = payload.Embeds[0].Description
		}
		received <- item
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"discord-message-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelDiscord,
		URL: server.URL + "/api/webhooks/1/token", Enabled: true,
		Events:        []string{models.NotificationEventWatchProgress, models.NotificationEventWatchWatched},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate, IncludeProfileName: true, IncludeDeviceName: true,
	}
	service := New(repo, WithProfiles(notificationProfiles{"profile": {Name: "godver3"}}), WithClients(notificationClients{client: &models.Client{Nickname: "Living Room TV", Name: "Display"}}))
	defer service.Close()

	update := models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:1", MovieName: "Movie", Duration: 100, ClientID: "device",
	}
	service.HandlePlaybackUpdate("profile", update, 0)
	first := waitForNotificationRequest(t, received)
	if first.method != http.MethodPost || first.path != "/api/webhooks/1/token" || first.query != "wait=true" {
		t.Fatalf("initial request = %s %s?%s", first.method, first.path, first.query)
	}
	if first.title != "godver3 - Living Room TV - Watching: Movie" || !strings.Contains(first.body, "0%") ||
		!strings.Contains(first.body, "▱") {
		t.Fatalf("initial progress payload = title %q body %q", first.title, first.body)
	}
	if strings.Count(first.body, "0%") != 1 {
		t.Fatalf("initial progress body repeats percentage: %q", first.body)
	}

	service.HandlePlaybackUpdate("profile", update, 0.9)
	select {
	case item := <-received:
		t.Fatalf("same whole-percentage progress bucket emitted %s %s", item.method, item.path)
	case <-time.After(75 * time.Millisecond):
	}

	service.HandlePlaybackUpdate("profile", update, 2)
	second := waitForNotificationRequest(t, received)
	if second.method != http.MethodPatch ||
		second.path != "/api/webhooks/1/token/messages/discord-message-1" {
		t.Fatalf("progress update request = %s %s", second.method, second.path)
	}
	if !strings.Contains(second.body, "2%") {
		t.Fatalf("updated progress body = %q", second.body)
	}

	service.HandlePlaybackUpdate("profile", update, 95)
	completed := waitForNotificationRequest(t, received)
	if completed.method != http.MethodPatch ||
		completed.path != "/api/webhooks/1/token/messages/discord-message-1" {
		t.Fatalf("completion request = %s %s", completed.method, completed.path)
	}
	if completed.title != "godver3 - Living Room TV - Watched: Movie" || strings.Contains(completed.body, "%") {
		t.Fatalf("completion payload = title %q body %q", completed.title, completed.body)
	}

	// A replacement stream can briefly report zero while seeking back to the
	// watched position. Never reopen progress after this playback session has
	// already completed.
	service.HandlePlaybackUpdate("profile", update, 0)
	select {
	case item := <-received:
		t.Fatalf("post-completion progress emitted %s %s", item.method, item.path)
	case <-time.After(75 * time.Millisecond):
	}
}

func TestDiscordProgressNotificationDeletesWhenPlaybackEndsUnfinished(t *testing.T) {
	type request struct {
		method string
		path   string
	}
	received := make(chan request, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- request{method: r.Method, path: r.URL.Path}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"discord-message-2"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelDiscord,
		URL: server.URL + "/api/webhooks/1/token", Enabled: true,
		Events:        []string{models.NotificationEventWatchProgress},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	update := models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:1", MovieName: "Movie", Duration: 100,
	}
	service.HandlePlaybackUpdate("profile", update, 20)
	_ = waitForNotificationRequest(t, received)
	update.IsPaused = true
	update.PlaybackEnded = true
	service.HandlePlaybackUpdate("profile", update, 20)
	deleted := waitForNotificationRequest(t, received)
	if deleted.method != http.MethodDelete ||
		deleted.path != "/api/webhooks/1/token/messages/discord-message-2" {
		t.Fatalf("stop request = %s %s", deleted.method, deleted.path)
	}
}

func TestDiscordProgressNotificationDeletesAfterHeartbeatTimeout(t *testing.T) {
	type request struct {
		method string
		path   string
	}
	received := make(chan request, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- request{method: r.Method, path: r.URL.Path}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"discord-stale-message"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelDiscord,
		URL: server.URL + "/api/webhooks/1/token", Enabled: true,
		Events:        []string{models.NotificationEventWatchProgress},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	update := models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:1", MovieName: "Movie", Duration: 100,
	}
	service.HandlePlaybackUpdate("profile", update, 20)
	_ = waitForNotificationRequest(t, received)

	service.reapStalePlaybackSessions(time.Now().UTC().Add(progressHeartbeatTimeout))
	deleted := waitForNotificationRequest(t, received)
	if deleted.method != http.MethodDelete ||
		deleted.path != "/api/webhooks/1/token/messages/discord-stale-message" {
		t.Fatalf("stale-session request = %s %s", deleted.method, deleted.path)
	}
}

func TestDiscordProgressNotificationIsSharedAcrossOverlappingPlaybackSessions(t *testing.T) {
	type request struct {
		method string
		path   string
	}
	received := make(chan request, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- request{method: r.Method, path: r.URL.Path}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"discord-shared-message"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelDiscord,
		URL: server.URL + "/api/webhooks/1/token", Enabled: true,
		Events:        []string{models.NotificationEventWatchProgress},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	first := models.PlaybackProgressUpdate{
		MediaType: "episode", ItemID: "tvdb:123:3:3", SeriesName: "Series",
		SeasonNumber: 3, EpisodeNumber: 3, Duration: 100, PlaybackSessionID: "first",
	}
	service.HandlePlaybackUpdate("profile", first, 0)
	created := waitForNotificationRequest(t, received)
	if created.method != http.MethodPost {
		t.Fatalf("initial request = %s %s", created.method, created.path)
	}
	waitForDurableProgressMessage(t, repo, "channel", notificationPlaybackKey(first))

	// Simulate an old durable timestamp while the original process still owns
	// the message. A second playback session must adopt the same in-memory
	// message instead of replacing the durable row and orphaning the new post.
	repo.mu.Lock()
	recordID := progressMessageID("channel", notificationPlaybackKey(first))
	record := repo.progress[recordID]
	record.UpdatedAt = time.Now().Add(-progressHeartbeatTimeout)
	repo.progress[recordID] = record
	repo.mu.Unlock()

	second := first
	second.PlaybackSessionID = "second"
	service.HandlePlaybackUpdate("profile", second, 1)
	updated := waitForNotificationRequest(t, received)
	if updated.method != http.MethodPatch ||
		updated.path != "/api/webhooks/1/token/messages/discord-shared-message" {
		t.Fatalf("overlapping-session update = %s %s", updated.method, updated.path)
	}

	first.PlaybackEnded = true
	first.IsPaused = true
	service.HandlePlaybackUpdate("profile", first, 1)
	select {
	case item := <-received:
		t.Fatalf("first overlapping stop emitted %s %s", item.method, item.path)
	case <-time.After(75 * time.Millisecond):
	}

	second.PlaybackEnded = true
	second.IsPaused = true
	service.HandlePlaybackUpdate("profile", second, 1)
	deleted := waitForNotificationRequest(t, received)
	if deleted.method != http.MethodDelete ||
		deleted.path != "/api/webhooks/1/token/messages/discord-shared-message" {
		t.Fatalf("final overlapping stop = %s %s", deleted.method, deleted.path)
	}
}

func TestDurableDiscordProgressSkippedAtStartupCanBeReapedLater(t *testing.T) {
	type request struct {
		method string
		path   string
	}
	received := make(chan request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- request{method: r.Method, path: r.URL.Path}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	now := time.Now().UTC()
	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelDiscord,
		URL: server.URL + "/api/webhooks/1/token", Enabled: true,
		Events: []string{models.NotificationEventWatchProgress},
	}
	repo.progress[progressMessageID("channel", "episode:tvdb%3A123%3A3%3A3")] = models.NotificationProgressMessage{
		ChannelID: "channel", ProfileID: "profile",
		PlaybackKey: "episode:tvdb%3A123%3A3%3A3", MessageID: "discord-restart-message",
		UpdatedAt: now,
	}

	service := New(repo)
	defer service.Close()
	select {
	case item := <-received:
		t.Fatalf("fresh startup record unexpectedly emitted %s %s", item.method, item.path)
	case <-time.After(75 * time.Millisecond):
	}

	service.reapDurableProgressMessages(now.Add(progressHeartbeatTimeout))
	deleted := waitForNotificationRequest(t, received)
	if deleted.method != http.MethodDelete ||
		deleted.path != "/api/webhooks/1/token/messages/discord-restart-message" {
		t.Fatalf("periodic durable cleanup = %s %s", deleted.method, deleted.path)
	}
}

func TestDiscordProgressNotificationSurvivesServiceRestart(t *testing.T) {
	type request struct {
		method string
		path   string
	}
	received := make(chan request, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- request{method: r.Method, path: r.URL.Path}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"discord-durable-message"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelDiscord,
		URL: server.URL + "/api/webhooks/1/token", Enabled: true,
		Events:        []string{models.NotificationEventWatchProgress},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	update := models.PlaybackProgressUpdate{
		MediaType: "movie", ItemID: "tmdb:1", MovieName: "Movie", Duration: 100,
	}

	firstService := New(repo)
	firstService.HandlePlaybackUpdate("profile", update, 20)
	created := waitForNotificationRequest(t, received)
	if created.method != http.MethodPost {
		t.Fatalf("initial durable request = %s %s", created.method, created.path)
	}
	waitForDurableProgressMessage(t, repo, "channel", notificationPlaybackKey(update))
	firstService.Close()

	secondService := New(repo)
	defer secondService.Close()
	secondService.HandlePlaybackUpdate("profile", update, 21)
	adopted := waitForNotificationRequest(t, received)
	if adopted.method != http.MethodPatch ||
		adopted.path != "/api/webhooks/1/token/messages/discord-durable-message" {
		t.Fatalf("post-restart request = %s %s", adopted.method, adopted.path)
	}
}

func waitForDurableProgressMessage(t *testing.T, repo *memoryRepo, channelID, playbackKey string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		message, err := repo.GetProgressMessage(context.Background(), channelID, playbackKey)
		if err != nil {
			t.Fatalf("get durable progress message: %v", err)
		}
		if message != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for durable progress message")
}

func TestProgressNotificationsRequireDiscord(t *testing.T) {
	service := New(newMemoryRepo())
	defer service.Close()

	_, err := service.SaveChannel(context.Background(), models.NotificationChannel{
		ProfileID: "profile",
		Name:      "Generic",
		Type:      models.NotificationChannelWebhook,
		URL:       "https://example.com/webhook",
		Events:    []string{models.NotificationEventWatchProgress},
	})
	if err == nil || !strings.Contains(err.Error(), "require a Discord destination") {
		t.Fatalf("SaveChannel error = %v", err)
	}
}

func waitForNotificationRequest[T any](t *testing.T, received <-chan T) T {
	t.Helper()
	select {
	case item := <-received:
		return item
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notification request")
		var zero T
		return zero
	}
}

func TestObserveCalendarReleasesDurableDueObservation(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true, Events: []string{models.NotificationEventRelease},
		NotifyWatchlist: true, ReleaseTypes: []string{"digital"},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	repo.observations[observationID("profile", "due")] = models.NotificationObservation{
		ProfileID: "profile", ItemKey: "due", Status: "upcoming",
		Event: models.NotificationEvent{
			ID: "release", Type: models.NotificationEventRelease, ProfileID: "profile",
			Title: "Due Movie", MediaType: "movie", ReleaseType: "digital",
			ReleaseDate: time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
			Source:      "watchlist", OccurredAt: time.Now().UTC(),
		},
	}
	service := New(repo)
	defer service.Close()
	service.ObserveCalendar("profile", nil)

	select {
	case actual := <-received:
		if actual != models.NotificationEventRelease {
			t.Fatalf("event = %q", actual)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for release event")
	}

	observation := repo.observations[observationID("profile", "due")]
	if observation.Status != "released" {
		t.Fatalf("status = %q", observation.Status)
	}
}

func TestObserveCalendarDoesNotNotifyUnclassifiedAvailabilityWhenReleaseTypesAreSelected(t *testing.T) {
	received := make(chan models.NotificationEvent, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Data models.NotificationEvent `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		received <- payload.Data
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := newMemoryRepo()
	repo.channels["channel"] = models.NotificationChannel{
		ID: "channel", ProfileID: "profile", Type: models.NotificationChannelWebhook,
		URL: server.URL, Enabled: true, Events: []string{models.NotificationEventRelease},
		NotifyTrending: true, TrendingLimit: 20, ReleaseTypes: []string{"digital", "physical"},
		TitleTemplate: defaultTitleTemplate, BodyTemplate: defaultBodyTemplate,
	}
	service := New(repo)
	defer service.Close()

	existing := models.CalendarItem{
		Title: "Existing Release", MediaType: "movie", Year: 2026,
		ReleaseType: "availability", ReleaseStatus: "released",
		Source: "top-trending", SourceRank: 1,
		ExternalIDs: map[string]string{"tmdb": "1"},
	}
	service.ObserveCalendar("profile", []models.CalendarItem{existing})
	select {
	case event := <-received:
		t.Fatalf("initial trending baseline emitted %q", event.Title)
	case <-time.After(100 * time.Millisecond):
	}
	baseline := repo.observations[observationID("profile", trendingReleaseBaselineKey)]
	if baseline.Status != "established" {
		t.Fatalf("trending baseline status = %q", baseline.Status)
	}

	newRelease := models.CalendarItem{
		Title: "Direct-to-Streaming Release", MediaType: "movie", Year: 2026,
		ReleaseType: "availability", ReleaseStatus: "released",
		Source: "top-trending", SourceRank: 2,
		ExternalIDs: map[string]string{"tmdb": "2"},
	}
	service.ObserveCalendar("profile", []models.CalendarItem{existing, newRelease})
	select {
	case event := <-received:
		t.Fatalf("unclassified availability emitted %q", event.Title)
	case <-time.After(150 * time.Millisecond):
	}

	service.ObserveCalendar("profile", []models.CalendarItem{existing, newRelease})
	select {
	case event := <-received:
		t.Fatalf("unchanged trending snapshot emitted duplicate %q", event.Title)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMigratedPlaybackSessionDoesNotRepeatWatched(t *testing.T) {
	service := New(newMemoryRepo())
	defer service.Close()

	update := models.PlaybackProgressUpdate{
		MediaType:         "episode",
		ItemID:            "tvdb:1:s1e1",
		SeriesName:        "Show",
		PlaybackSessionID: "direct:first-source",
	}
	service.HandlePlaybackUpdate("profile", update, 95)

	// Stream migration: new source path, new session, resumed near the end.
	update.PlaybackSessionID = "direct:migrated-source"
	service.HandlePlaybackUpdate("profile", update, 96)
	// Rewatch from the start is a fresh viewing and still notifies.
	update.PlaybackSessionID = "direct:rewatch"
	service.HandlePlaybackUpdate("profile", update, 1)

	service.sessionMu.Lock()
	defer service.sessionMu.Unlock()
	if s := service.sessions["profile\x00direct:migrated-source"]; !s.suppressed || !s.watched {
		t.Fatalf("migrated session = %+v, want suppressed and watched", s)
	}
	if s := service.sessions["profile\x00direct:rewatch"]; s.suppressed || s.watched {
		t.Fatalf("rewatch session = %+v, want active", s)
	}
}
