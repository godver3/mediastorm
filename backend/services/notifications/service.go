package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/calendar"
)

const (
	defaultTitleTemplate = "{{eventLabel}}: {{title}}"
	defaultBodyTemplate  = "{{mediaLabel}}{{progressLabel}}{{releaseLabel}}"
)

var defaultReleaseTypes = []string{"digital", "physical"}

var validEvents = map[string]bool{
	models.NotificationEventWatchStarted:   true,
	models.NotificationEventWatchProgress:  true,
	models.NotificationEventWatchResumed:   true,
	models.NotificationEventWatchWatched:   true,
	models.NotificationEventRelease:        true,
	models.NotificationEventSystemStartup:  true,
	models.NotificationEventSystemShutdown: true,
}

var validReleaseTypes = map[string]string{
	"digital":           "digital",
	"physical":          "physical",
	"theatrical":        "theatrical",
	"theatricallimited": "theatricalLimited",
	"premiere":          "premiere",
	"tv":                "tv",
}

const trendingReleaseBaselineKey = "__baseline__:trending-releases"

type delivery struct {
	channel   models.NotificationChannel
	event     models.NotificationEvent
	action    string
	session   string
	recordKey string
	sequence  uint64
}

type playbackSession struct {
	seen           bool
	paused         bool
	watched        bool
	suppressed     bool
	progressSent   bool
	progressBucket int
	profileID      string
	playbackKey    string
	notificationID string
	sequence       uint64
	persistedAt    time.Time
	updatedAt      time.Time
}

const (
	deliveryProgressUpsert           = "progress.upsert"
	deliveryProgressComplete         = "progress.complete"
	deliveryProgressDelete           = "progress.delete"
	progressStepPercent              = 1
	progressHeartbeatTimeout         = 2 * time.Minute
	progressHeartbeatPersistInterval = 30 * time.Second
	progressReapInterval             = 15 * time.Second
	// A session that resumes an item already marked watched (stream migration,
	// player reopen) must not announce it again. Rewatches start from the top,
	// so only sessions that begin past this point are treated as continuations.
	recentWatchedWindow        = 6 * time.Hour
	recentWatchedResumePercent = 50
)

type profileProvider interface {
	Get(string) (models.User, bool)
}

type clientProvider interface {
	Get(string) (*models.Client, error)
}

type Option func(*Service)

func WithProfiles(profiles profileProvider) Option {
	return func(s *Service) { s.profiles = profiles }
}

func WithClients(clients clientProvider) Option {
	return func(s *Service) { s.clients = clients }
}

// Service owns profile notification configuration, formatting, and delivery.
type Service struct {
	profiles   profileProvider
	clients    clientProvider
	repo       datastore.NotificationRepository
	httpClient *http.Client
	deliveries chan delivery
	stop       chan struct{}

	sessionMu sync.Mutex
	sessions  map[string]playbackSession
	// recentWatched maps profile+playback key to when Watched last fired.
	recentWatched map[string]time.Time
	observeMu     sync.Mutex
	deliveredMu   sync.Mutex
	delivered     map[string]time.Time

	progressMessages  map[string]string
	progressSequences map[string]uint64
	progressUpdated   map[string]time.Time
}

func New(repo datastore.NotificationRepository, options ...Option) *Service {
	s := &Service{
		repo: repo,
		httpClient: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: sameHostRedirect,
		},
		deliveries:        make(chan delivery, 256),
		stop:              make(chan struct{}),
		sessions:          make(map[string]playbackSession),
		recentWatched:     make(map[string]time.Time),
		delivered:         make(map[string]time.Time),
		progressMessages:  make(map[string]string),
		progressSequences: make(map[string]uint64),
		progressUpdated:   make(map[string]time.Time),
	}
	for _, option := range options {
		option(s)
	}
	go s.run()
	return s
}

func (s *Service) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *Service) ListChannels(ctx context.Context, profileID string) ([]models.NotificationChannel, error) {
	channels, err := s.repo.ListChannels(ctx, strings.TrimSpace(profileID))
	if err != nil {
		return nil, err
	}
	for i := range channels {
		channels[i].URLConfigured = channels[i].URL != ""
		channels[i].URL = ""
	}
	return channels, nil
}

func (s *Service) SaveChannel(ctx context.Context, channel models.NotificationChannel) (models.NotificationChannel, error) {
	channel.ID = strings.TrimSpace(channel.ID)
	channel.ProfileID = strings.TrimSpace(channel.ProfileID)
	channel.Name = strings.TrimSpace(channel.Name)
	channel.Type = strings.ToLower(strings.TrimSpace(channel.Type))
	channel.URL = strings.TrimSpace(channel.URL)
	channel.TitleTemplate = strings.TrimSpace(channel.TitleTemplate)
	channel.BodyTemplate = strings.TrimSpace(channel.BodyTemplate)

	if channel.ProfileID == "" {
		return models.NotificationChannel{}, errors.New("profile ID is required")
	}
	if channel.Name == "" {
		return models.NotificationChannel{}, errors.New("name is required")
	}
	if channel.Type != models.NotificationChannelDiscord && channel.Type != models.NotificationChannelWebhook {
		return models.NotificationChannel{}, errors.New("type must be discord or webhook")
	}
	channel.Events = normalizeEvents(channel.Events)
	if len(channel.Events) == 0 {
		return models.NotificationChannel{}, errors.New("select at least one event")
	}
	if contains(channel.Events, models.NotificationEventWatchProgress) &&
		channel.Type != models.NotificationChannelDiscord {
		return models.NotificationChannel{}, errors.New("progress notifications require a Discord destination")
	}
	hasReleaseEvents := contains(channel.Events, models.NotificationEventRelease)
	if hasReleaseEvents && len(channel.Events) > 1 {
		return models.NotificationChannel{}, errors.New("watch status and release status events must use separate destinations")
	}
	hasSystemEvents := contains(channel.Events, models.NotificationEventSystemStartup) ||
		contains(channel.Events, models.NotificationEventSystemShutdown)
	if hasSystemEvents && len(channel.Events) > countSystemEvents(channel.Events) {
		return models.NotificationChannel{}, errors.New("system operations and media events must use separate destinations")
	}
	if hasReleaseEvents {
		if !channel.NotifyWatchlist && !channel.NotifyTrending {
			return models.NotificationChannel{}, errors.New("select at least one release source")
		}
		if channel.ReleaseTypes == nil {
			channel.ReleaseTypes = append([]string(nil), defaultReleaseTypes...)
		} else {
			channel.ReleaseTypes = normalizeReleaseTypes(channel.ReleaseTypes)
			if len(channel.ReleaseTypes) == 0 {
				return models.NotificationChannel{}, errors.New("select at least one release type")
			}
		}
	} else {
		channel.NotifyWatchlist = false
		channel.NotifyTrending = false
		channel.ReleaseTypes = []string{}
	}
	if channel.TrendingLimit < 1 {
		channel.TrendingLimit = 20
	}
	if channel.TrendingLimit > 100 {
		channel.TrendingLimit = 100
	}
	if channel.TitleTemplate == "" {
		channel.TitleTemplate = defaultTitleTemplate
	}
	if channel.BodyTemplate == "" {
		channel.BodyTemplate = defaultBodyTemplate
	}

	now := time.Now().UTC()
	if channel.ID == "" {
		if err := validateDestination(channel.Type, channel.URL); err != nil {
			return models.NotificationChannel{}, err
		}
		channel.ID = uuid.NewString()
		channel.CreatedAt = now
		channel.UpdatedAt = now
		channel.URLConfigured = true
		if err := s.repo.CreateChannel(ctx, &channel); err != nil {
			return models.NotificationChannel{}, fmt.Errorf("create notification channel: %w", err)
		}
	} else {
		existing, err := s.repo.GetChannel(ctx, channel.ID)
		if err != nil {
			return models.NotificationChannel{}, fmt.Errorf("load notification channel: %w", err)
		}
		if existing == nil || existing.ProfileID != channel.ProfileID {
			return models.NotificationChannel{}, errors.New("notification channel not found")
		}
		if channel.URL == "" {
			channel.URL = existing.URL
		}
		if err := validateDestination(channel.Type, channel.URL); err != nil {
			return models.NotificationChannel{}, err
		}
		channel.CreatedAt = existing.CreatedAt
		channel.UpdatedAt = now
		channel.URLConfigured = channel.URL != ""
		if err := s.repo.UpdateChannel(ctx, &channel); err != nil {
			return models.NotificationChannel{}, fmt.Errorf("update notification channel: %w", err)
		}
	}

	channel.URL = ""
	return channel, nil
}

func (s *Service) DeleteChannel(ctx context.Context, profileID, id string) error {
	return s.repo.DeleteChannel(ctx, strings.TrimSpace(profileID), strings.TrimSpace(id))
}

func (s *Service) TestChannel(ctx context.Context, profileID, id string) error {
	channel, err := s.repo.GetChannel(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if channel == nil || channel.ProfileID != strings.TrimSpace(profileID) {
		return errors.New("notification channel not found")
	}
	event := models.NotificationEvent{
		ID:         uuid.NewString(),
		Type:       models.NotificationEventWatchStarted,
		ProfileID:  channel.ProfileID,
		Title:      "Notification test",
		MediaType:  "movie",
		Percent:    42,
		OccurredAt: time.Now().UTC(),
	}
	if contains(channel.Events, models.NotificationEventWatchProgress) {
		event.Type = models.NotificationEventWatchProgress
	} else if contains(channel.Events, models.NotificationEventRelease) {
		event.Type = models.NotificationEventRelease
		event.ReleaseType = "digital"
		event.ReleaseDate = event.OccurredAt.Format("2006-01-02")
		event.Source = "test"
	} else if contains(channel.Events, models.NotificationEventSystemStartup) {
		event.Type = models.NotificationEventSystemStartup
		event.Title = "mediastorm"
		event.MediaType = "system"
		event.Percent = 0
	} else if contains(channel.Events, models.NotificationEventSystemShutdown) {
		event.Type = models.NotificationEventSystemShutdown
		event.Title = "mediastorm"
		event.MediaType = "system"
		event.Percent = 0
	}
	return s.deliver(ctx, *channel, event)
}

// NotifySystem synchronously sends a lifecycle event to every subscribed
// destination. Lifecycle delivery intentionally bypasses the background queue:
// during shutdown the process must wait for outbound requests before Docker's
// stop grace period expires and networking is removed.
func (s *Service) NotifySystem(ctx context.Context, eventType string) error {
	if eventType != models.NotificationEventSystemStartup &&
		eventType != models.NotificationEventSystemShutdown {
		return fmt.Errorf("unsupported system notification event %q", eventType)
	}
	channels, err := s.repo.ListAllChannels(ctx)
	if err != nil {
		return fmt.Errorf("list system notification channels: %w", err)
	}

	var wg sync.WaitGroup
	var errorsMu sync.Mutex
	var deliveryErrors []error
	for _, channel := range channels {
		if !channel.Enabled || !contains(channel.Events, eventType) {
			continue
		}
		channel := channel
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := models.NotificationEvent{
				ID:         uuid.NewString(),
				Type:       eventType,
				ProfileID:  channel.ProfileID,
				Title:      "mediastorm",
				MediaType:  "system",
				OccurredAt: time.Now().UTC(),
			}
			if err := s.deliver(ctx, channel, event); err != nil {
				errorsMu.Lock()
				deliveryErrors = append(deliveryErrors,
					fmt.Errorf("channel %s: %w", channel.ID, err))
				errorsMu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(deliveryErrors...)
}

// HandlePlaybackUpdate converts player heartbeats into edge-triggered watch events.
func (s *Service) HandlePlaybackUpdate(userID string, update models.PlaybackProgressUpdate, percent float64) {
	if isNonNotifiablePlayback(update) {
		return
	}
	key := userID + "\x00" + update.MediaType + "\x00" + update.ItemID
	if update.PlaybackSessionID != "" {
		key = userID + "\x00" + update.PlaybackSessionID
	}
	persistedPlaybackKey := notificationPlaybackKey(update)
	now := time.Now().UTC()
	active := !update.IsPaused && !update.IsBuffering

	s.sessionMu.Lock()
	state := s.sessions[key]
	if !state.updatedAt.IsZero() && now.Sub(state.updatedAt) > 30*time.Minute {
		state = playbackSession{}
	}
	watchedKey := userID + "\x00" + persistedPlaybackKey
	if state.notificationID == "" {
		if at, ok := s.recentWatched[watchedKey]; ok && now.Sub(at) < recentWatchedWindow &&
			percent >= recentWatchedResumePercent {
			state.suppressed = true
			state.seen = true
			state.watched = true
		}
		state.profileID = userID
		state.playbackKey = persistedPlaybackKey
		state.notificationID = uuid.NewString()
	}
	state.sequence++
	notificationSession := key + "\x00" + state.notificationID
	sequence := state.sequence
	var eventTypes []string
	if active && !state.seen {
		eventTypes = append(eventTypes, models.NotificationEventWatchStarted)
		state.seen = true
	} else if active && state.paused && !state.suppressed {
		eventTypes = append(eventTypes, models.NotificationEventWatchResumed)
	}
	if percent >= 90 && !state.watched {
		eventTypes = append(eventTypes, models.NotificationEventWatchWatched)
		state.watched = true
		s.recentWatched[watchedKey] = now
	} else if active && percent < 90 && !state.watched {
		progressBucket := int(percent) / progressStepPercent
		if !state.progressSent || progressBucket != state.progressBucket {
			eventTypes = append(eventTypes, models.NotificationEventWatchProgress)
			state.progressSent = true
			state.progressBucket = progressBucket
			if state.persistedAt.IsZero() {
				state.persistedAt = now
			}
		}
	}
	touchPersistedHeartbeat := now.Sub(state.persistedAt) >= progressHeartbeatPersistInterval
	if touchPersistedHeartbeat {
		state.persistedAt = now
	}
	state.paused = update.IsPaused || update.IsBuffering
	state.updatedAt = now
	deleteUnfinishedProgress := update.PlaybackEnded && percent < 90
	if update.PlaybackEnded {
		delete(s.sessions, key)
		for _, other := range s.sessions {
			if other.profileID == userID && other.playbackKey == persistedPlaybackKey &&
				other.progressSent && !other.watched {
				deleteUnfinishedProgress = false
				break
			}
		}
	} else {
		s.sessions[key] = state
	}
	s.pruneSessionsLocked(now)
	s.sessionMu.Unlock()

	if touchPersistedHeartbeat {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.repo.TouchProgressMessages(ctx, userID, persistedPlaybackKey, now); err != nil {
			log.Printf("[notifications] touch progress messages profile=%s: %v", userID, err)
		}
		cancel()
	}

	for _, eventType := range eventTypes {
		eventPercent := percent
		if eventType == models.NotificationEventWatchWatched {
			eventPercent = 0
		}
		event := models.NotificationEvent{
			ID:            uuid.NewString(),
			Type:          eventType,
			ProfileID:     userID,
			ClientID:      update.ClientID,
			Title:         playbackTitle(update),
			MediaType:     update.MediaType,
			Year:          update.Year,
			SeriesTitle:   update.SeriesName,
			EpisodeTitle:  update.EpisodeName,
			SeasonNumber:  update.SeasonNumber,
			EpisodeNumber: update.EpisodeNumber,
			Position:      update.Position,
			Duration:      update.Duration,
			Percent:       eventPercent,
			PosterURL:     firstNonEmpty(update.NotificationImageURL, update.PosterURL),
			ExternalIDs:   update.ExternalIDs,
			OccurredAt:    now,
		}
		if eventType == models.NotificationEventWatchProgress || eventType == models.NotificationEventWatchWatched {
			s.notifyPlaybackLifecycle(event, notificationSession, persistedPlaybackKey, sequence)
		} else {
			s.Notify(event)
		}
	}
	if deleteUnfinishedProgress {
		s.deletePlaybackProgressNotification(userID, notificationSession, persistedPlaybackKey, sequence)
	}
}

func isNonNotifiablePlayback(update models.PlaybackProgressUpdate) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(update.SourcePath)), "recording:") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(update.MediaType)) {
	case "live", "livetv", "live-tv", "channel", "channels":
		return true
	default:
		return false
	}
}

func (s *Service) notifyPlaybackLifecycle(event models.NotificationEvent, session, recordKey string, sequence uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	channels, err := s.repo.ListChannels(ctx, event.ProfileID)
	if err != nil {
		log.Printf("[notifications] list playback channels profile=%s: %v", event.ProfileID, err)
		return
	}
	activeProgressChannels := make(map[string]bool)
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		action := deliveryProgressUpsert
		if event.Type == models.NotificationEventWatchWatched {
			if contains(channel.Events, models.NotificationEventWatchProgress) && channel.Type == models.NotificationChannelDiscord {
				action = deliveryProgressComplete
				activeProgressChannels[channel.ID] = true
			} else if contains(channel.Events, models.NotificationEventWatchWatched) {
				s.enqueueDelivery(delivery{channel: channel, event: event})
				continue
			} else {
				continue
			}
			s.enqueueDelivery(delivery{channel: channel, event: event, action: action, session: session, recordKey: recordKey, sequence: sequence})
			continue
		}
		if !contains(channel.Events, models.NotificationEventWatchProgress) ||
			channel.Type != models.NotificationChannelDiscord {
			continue
		}
		activeProgressChannels[channel.ID] = true
		s.enqueueDelivery(delivery{channel: channel, event: event, action: action, session: session, recordKey: recordKey, sequence: sequence})
	}
	s.enqueueInactiveProgressDeletes(ctx, event.ProfileID, session, recordKey, sequence, activeProgressChannels)
}

func (s *Service) deletePlaybackProgressNotification(profileID, session, recordKey string, sequence uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	channels, err := s.repo.ListChannels(ctx, profileID)
	if err != nil {
		log.Printf("[notifications] list playback channels profile=%s: %v", profileID, err)
		return
	}
	enqueued := make(map[string]bool)
	for _, channel := range channels {
		if channel.Enabled && channel.Type == models.NotificationChannelDiscord &&
			contains(channel.Events, models.NotificationEventWatchProgress) {
			s.enqueueDelivery(delivery{channel: channel, action: deliveryProgressDelete, session: session, recordKey: recordKey, sequence: sequence})
			enqueued[channel.ID] = true
		}
	}
	s.enqueueInactiveProgressDeletes(ctx, profileID, session, recordKey, sequence, enqueued)
}

func (s *Service) enqueueInactiveProgressDeletes(
	ctx context.Context,
	profileID, session, recordKey string,
	sequence uint64,
	excludedChannels map[string]bool,
) {
	messages, err := s.repo.ListProgressMessagesByPlayback(ctx, profileID, recordKey)
	if err != nil {
		log.Printf("[notifications] list durable progress messages profile=%s: %v", profileID, err)
		return
	}
	for _, message := range messages {
		if excludedChannels[message.ChannelID] {
			continue
		}
		channel, err := s.repo.GetChannel(ctx, message.ChannelID)
		if err != nil {
			log.Printf("[notifications] load durable progress channel=%s: %v", message.ChannelID, err)
			continue
		}
		if channel == nil {
			if err := s.repo.DeleteProgressMessage(ctx, message.ChannelID, recordKey); err != nil {
				log.Printf("[notifications] delete orphaned progress record channel=%s: %v", message.ChannelID, err)
			}
			continue
		}
		if channel.Type != models.NotificationChannelDiscord {
			if err := s.repo.DeleteProgressMessage(ctx, message.ChannelID, recordKey); err != nil {
				log.Printf("[notifications] delete non-Discord progress record channel=%s: %v", message.ChannelID, err)
			}
			continue
		}
		s.enqueueDelivery(delivery{
			channel:   *channel,
			action:    deliveryProgressDelete,
			session:   session,
			recordKey: recordKey,
			sequence:  sequence,
		})
	}
}

func (s *Service) enqueueDelivery(item delivery) {
	select {
	case s.deliveries <- item:
	default:
		log.Printf("[notifications] delivery queue full; dropping action=%s event=%s profile=%s",
			item.action, item.event.Type, item.event.ProfileID)
	}
}

func (s *Service) pruneSessionsLocked(now time.Time) {
	if len(s.sessions) >= 256 {
		for key, state := range s.sessions {
			if now.Sub(state.updatedAt) > 30*time.Minute {
				delete(s.sessions, key)
			}
		}
	}
	for key, at := range s.recentWatched {
		if now.Sub(at) > recentWatchedWindow {
			delete(s.recentWatched, key)
		}
	}
}

// ObserveCalendar establishes a baseline, then emits release events when an
// already-observed upcoming item becomes available.
func (s *Service) ObserveCalendar(profileID string, items []models.CalendarItem) {
	s.observeMu.Lock()
	defer s.observeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	today := time.Now().UTC().Format("2006-01-02")
	observations, err := s.repo.ListObservations(ctx, profileID)
	if err != nil {
		log.Printf("[notifications] list release observations profile=%s: %v", profileID, err)
		return
	}
	observationsByKey := make(map[string]models.NotificationObservation, len(observations))
	for _, observation := range observations {
		observationsByKey[observation.ItemKey] = observation
	}
	requirements := s.ReleaseRequirements(profileID)
	trendingBaseline, hasTrendingBaseline := observationsByKey[trendingReleaseBaselineKey]
	trendingBaselineEstablished := hasTrendingBaseline && trendingBaseline.Status == "established"
	for _, item := range items {
		if item.Source != "watchlist" && item.Source != "top-trending" && item.Source != "trending" {
			continue
		}
		key := releaseObservationKey(item)
		previous, hadPrevious := observationsByKey[key]
		event := models.NotificationEvent{
			ID:            uuid.NewString(),
			Type:          models.NotificationEventRelease,
			ProfileID:     profileID,
			Title:         item.Title,
			MediaType:     item.MediaType,
			Year:          item.Year,
			EpisodeTitle:  item.EpisodeTitle,
			SeasonNumber:  item.SeasonNumber,
			EpisodeNumber: item.EpisodeNumber,
			ReleaseType:   item.ReleaseType,
			ReleaseDate:   item.AirDate,
			Source:        item.Source,
			SourceRank:    item.SourceRank,
			PosterURL:     notificationReleaseArtwork(item),
			ExternalIDs:   item.ExternalIDs,
			OccurredAt:    time.Now().UTC(),
		}
		status := "upcoming"
		if item.ReleaseStatus == "released" {
			status = "released"
		} else if item.ReleaseStatus == "" && item.AirDate != "" && item.AirDate <= today {
			status = "released"
		}
		observation := &models.NotificationObservation{
			ProfileID: profileID,
			ItemKey:   key,
			Status:    status,
			Event:     event,
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.repo.UpsertObservation(ctx, observation); err != nil {
			log.Printf("[notifications] save release observation profile=%s: %v", profileID, err)
			continue
		}
		observationsByKey[key] = *observation
		becameReleased := hadPrevious && previous.Status != status && status == "released"
		enteredTrendingReleased := !hadPrevious && status == "released" && trendingBaselineEstablished &&
			(item.Source == "top-trending" || item.Source == "trending")
		if becameReleased || enteredTrendingReleased {
			s.Notify(event)
		}
	}
	if requirements.TrendingLimit > 0 && !trendingBaselineEstablished {
		baseline := models.NotificationObservation{
			ProfileID: profileID,
			ItemKey:   trendingReleaseBaselineKey,
			Status:    "established",
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.repo.UpsertObservation(ctx, &baseline); err != nil {
			log.Printf("[notifications] save trending release baseline profile=%s: %v", profileID, err)
		} else {
			observationsByKey[trendingReleaseBaselineKey] = baseline
		}
	}

	// Calendar builders intentionally drop releases after they become
	// available. Durable observations let us detect that transition even when
	// the released item is no longer present in the newly built calendar.
	for key, current := range observationsByKey {
		observation := current
		if observation.Status != "upcoming" || observation.Event.ReleaseDate == "" ||
			observation.Event.ReleaseDate > today {
			continue
		}
		observation.Status = "released"
		observation.UpdatedAt = time.Now().UTC()
		observation.Event.OccurredAt = observation.UpdatedAt
		if err := s.repo.UpsertObservation(ctx, &observation); err != nil {
			log.Printf("[notifications] release transition profile=%s: %v", profileID, err)
			continue
		}
		observationsByKey[key] = observation
		s.Notify(observation.Event)
	}
}

func (s *Service) Notify(event models.NotificationEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	channels, err := s.repo.ListChannels(ctx, event.ProfileID)
	cancel()
	if err != nil {
		log.Printf("[notifications] list channels profile=%s: %v", event.ProfileID, err)
		return
	}
	for _, channel := range channels {
		if !channel.Enabled || !contains(channel.Events, event.Type) || !channelAcceptsRelease(channel, event) {
			continue
		}
		if !s.claimDelivery(channel.ID, event) {
			continue
		}
		s.enqueueDelivery(delivery{channel: channel, event: event})
	}
}

func (s *Service) claimDelivery(channelID string, event models.NotificationEvent) bool {
	if event.Type != models.NotificationEventRelease {
		return true
	}
	identities := releaseEventIdentities(event)
	now := time.Now().UTC()
	s.deliveredMu.Lock()
	defer s.deliveredMu.Unlock()
	var matchedAt time.Time
	duplicate := false
	for _, identity := range identities {
		key := channelID + "\x00" + identity
		if deliveredAt, ok := s.delivered[key]; ok && now.Sub(deliveredAt) < 24*time.Hour {
			matchedAt = deliveredAt
			duplicate = true
			break
		}
	}
	if matchedAt.IsZero() {
		matchedAt = now
	}
	for _, identity := range identities {
		s.delivered[channelID+"\x00"+identity] = matchedAt
	}
	if duplicate {
		return false
	}
	if len(s.delivered) > 1024 {
		for existingKey, deliveredAt := range s.delivered {
			if now.Sub(deliveredAt) >= 24*time.Hour {
				delete(s.delivered, existingKey)
			}
		}
	}
	return true
}

// ReleaseRequirements tells the calendar worker which notification-only
// sources must be observed independently of the profile's calendar UI choices.
func (s *Service) ReleaseRequirements(profileID string) calendar.ReleaseRequirements {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	channels, err := s.repo.ListChannels(ctx, profileID)
	if err != nil {
		log.Printf("[notifications] load release requirements profile=%s: %v", profileID, err)
		return calendar.ReleaseRequirements{}
	}
	var requirements calendar.ReleaseRequirements
	for _, channel := range channels {
		if !channel.Enabled || !contains(channel.Events, models.NotificationEventRelease) {
			continue
		}
		if channel.NotifyWatchlist {
			requirements.Watchlist = true
		}
		if channel.NotifyTrending && channel.TrendingLimit > requirements.TrendingLimit {
			requirements.TrendingLimit = channel.TrendingLimit
		}
	}
	return requirements
}

func (s *Service) run() {
	s.reapDurableProgressMessages(time.Now().UTC())
	reaper := time.NewTicker(progressReapInterval)
	defer reaper.Stop()
	for {
		select {
		case item := <-s.deliveries:
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			var err error
			for attempt := 0; attempt < 3; attempt++ {
				err = s.deliverQueued(ctx, item)
				if err == nil {
					break
				}
				if attempt < 2 {
					select {
					case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
					case <-s.stop:
						cancel()
						return
					}
				}
			}
			if err != nil {
				log.Printf("[notifications] delivery failed channel=%s type=%s event=%s: %v",
					item.channel.ID, item.channel.Type, item.event.Type, err)
			}
			cancel()
		case now := <-reaper.C:
			s.reapStalePlaybackSessions(now.UTC())
			s.reapDurableProgressMessages(now.UTC())
		case <-s.stop:
			return
		}
	}
}

func (s *Service) reapStalePlaybackSessions(now time.Time) {
	type staleSession struct {
		profileID string
		session   string
		recordKey string
		sequence  uint64
	}
	staleByPlayback := make(map[string]staleSession)

	s.sessionMu.Lock()
	for key, state := range s.sessions {
		if state.updatedAt.IsZero() || now.Sub(state.updatedAt) < progressHeartbeatTimeout {
			continue
		}
		delete(s.sessions, key)
		if state.watched || !state.progressSent {
			continue
		}
		staleByPlayback[state.profileID+"\x00"+state.playbackKey] = staleSession{
			profileID: state.profileID,
			session:   key + "\x00" + state.notificationID,
			recordKey: state.playbackKey,
			sequence:  state.sequence + 1,
		}
	}
	for _, state := range s.sessions {
		if state.progressSent && !state.watched {
			delete(staleByPlayback, state.profileID+"\x00"+state.playbackKey)
		}
	}
	s.sessionMu.Unlock()

	for _, session := range staleByPlayback {
		s.deletePlaybackProgressNotification(session.profileID, session.session, session.recordKey, session.sequence)
	}
}

func (s *Service) reapDurableProgressMessages(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	messages, err := s.repo.ListProgressMessages(ctx)
	if err != nil {
		log.Printf("[notifications] list durable progress messages: %v", err)
		return
	}
	for _, message := range messages {
		if now.Sub(message.UpdatedAt) < progressHeartbeatTimeout {
			continue
		}
		channel, err := s.repo.GetChannel(ctx, message.ChannelID)
		if err != nil {
			log.Printf("[notifications] load durable progress channel=%s: %v", message.ChannelID, err)
			continue
		}
		if channel == nil {
			if err := s.repo.DeleteProgressMessage(ctx, message.ChannelID, message.PlaybackKey); err != nil {
				log.Printf("[notifications] delete orphaned progress record channel=%s: %v", message.ChannelID, err)
			}
			continue
		}
		s.enqueueDelivery(delivery{
			channel:   *channel,
			action:    deliveryProgressDelete,
			session:   "startup:" + uuid.NewString(),
			recordKey: message.PlaybackKey,
			sequence:  1,
		})
	}
}

func (s *Service) deliverQueued(ctx context.Context, item delivery) error {
	if item.action != "" {
		key := item.channel.ID + "\x00" + item.session
		if item.sequence <= s.progressSequences[key] {
			return nil
		}
	}
	var err error
	switch item.action {
	case deliveryProgressUpsert:
		err = s.upsertDiscordProgress(ctx, item, false)
	case deliveryProgressComplete:
		err = s.upsertDiscordProgress(ctx, item, true)
	case deliveryProgressDelete:
		err = s.deleteDiscordProgress(ctx, item)
	default:
		return s.deliver(ctx, item.channel, item.event)
	}
	if err == nil {
		key := item.channel.ID + "\x00" + item.session
		s.progressSequences[key] = item.sequence
		s.progressUpdated[key] = time.Now().UTC()
		s.pruneProgressDeliveries()
	}
	return err
}

func (s *Service) pruneProgressDeliveries() {
	if len(s.progressUpdated) < 256 {
		return
	}
	cutoff := time.Now().UTC().Add(-time.Hour)
	for key, updatedAt := range s.progressUpdated {
		if updatedAt.Before(cutoff) {
			delete(s.progressUpdated, key)
			delete(s.progressSequences, key)
		}
	}
}

func (s *Service) deliver(ctx context.Context, channel models.NotificationChannel, event models.NotificationEvent) error {
	event = s.withDeviceName(s.withProfileName(channel, event))
	title, body := Format(channel, event)
	var payload any
	if channel.Type == models.NotificationChannelDiscord {
		payload = discordPayload(channel, event)
	} else {
		payload = map[string]any{
			"event":   event.Type,
			"title":   title,
			"message": body,
			"data":    event,
		}
		if !channel.IncludePoster {
			payload.(map[string]any)["data"] = withoutPoster(event)
		}
	}
	bodyJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.URL, bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mediastorm-notifications/1.0")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("destination returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func discordPayload(channel models.NotificationChannel, event models.NotificationEvent) map[string]any {
	title, body := Format(channel, event)
	if event.Type == models.NotificationEventWatchProgress {
		body = strings.TrimSpace(body + "\n" + progressBar(event.Percent))
	}
	embed := map[string]any{
		"title":       truncate(title, 256),
		"description": truncate(body, 4096),
		"timestamp":   event.OccurredAt.Format(time.RFC3339),
	}
	if channel.IncludePoster {
		if posterURL := publicHTTPURL(event.PosterURL); posterURL != "" {
			embed["thumbnail"] = map[string]string{"url": posterURL}
		}
	}
	return map[string]any{"embeds": []any{embed}}
}

func progressBar(percent float64) string {
	const width = 20
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	filled := int((percent*width + 50) / 100)
	// Keep an active zero/low-percent bar from becoming one long run of hollow
	// parallelograms, which Discord renders wider than the mixed bar.
	if filled == 0 {
		filled = 1
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", width-filled)
}

func (s *Service) upsertDiscordProgress(ctx context.Context, item delivery, complete bool) error {
	key := progressMessageCacheKey(item.channel.ID, item.recordKey)
	messageID := s.progressMessages[key]
	if messageID == "" {
		record, err := s.repo.GetProgressMessage(ctx, item.channel.ID, item.recordKey)
		if err != nil {
			return fmt.Errorf("load durable Discord progress message: %w", err)
		}
		if record != nil {
			if time.Since(record.UpdatedAt) < progressHeartbeatTimeout {
				messageID = record.MessageID
				s.progressMessages[key] = messageID
			} else {
				if err := s.deleteDiscordWebhookMessage(ctx, item.channel.URL, record.MessageID); err != nil {
					return err
				}
				if err := s.repo.DeleteProgressMessage(ctx, item.channel.ID, item.recordKey); err != nil {
					return fmt.Errorf("delete stale Discord progress record: %w", err)
				}
			}
		}
	}
	item.event = s.withDeviceName(s.withProfileName(item.channel, item.event))
	payload := discordPayload(item.channel, item.event)
	bodyJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if messageID != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
			discordWebhookMessageURL(item.channel.URL, messageID, false), bytes.NewReader(bodyJSON))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "mediastorm-notifications/1.0")
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			delete(s.progressMessages, key)
			if err := s.repo.DeleteProgressMessage(ctx, item.channel.ID, item.recordKey); err != nil {
				return fmt.Errorf("delete missing Discord progress record: %w", err)
			}
			messageID = ""
		} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("destination returned HTTP %d", resp.StatusCode)
		} else if complete {
			if err := s.repo.DeleteProgressMessage(ctx, item.channel.ID, item.recordKey); err != nil {
				return fmt.Errorf("delete completed Discord progress record: %w", err)
			}
			delete(s.progressMessages, key)
			return nil
		} else {
			if err := s.persistDiscordProgressMessage(ctx, item, messageID); err != nil {
				return err
			}
			return nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		discordWebhookMessageURL(item.channel.URL, "", true), bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mediastorm-notifications/1.0")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("destination returned HTTP %d", resp.StatusCode)
	}
	if complete {
		return s.repo.DeleteProgressMessage(ctx, item.channel.ID, item.recordKey)
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&response); err != nil {
		return fmt.Errorf("decode Discord webhook response: %w", err)
	}
	if response.ID == "" {
		return errors.New("Discord webhook response did not include a message ID")
	}
	s.progressMessages[key] = response.ID
	return s.persistDiscordProgressMessage(ctx, item, response.ID)
}

func (s *Service) deleteDiscordProgress(ctx context.Context, item delivery) error {
	key := progressMessageCacheKey(item.channel.ID, item.recordKey)
	messageID := s.progressMessages[key]
	if messageID == "" {
		record, err := s.repo.GetProgressMessage(ctx, item.channel.ID, item.recordKey)
		if err != nil {
			return fmt.Errorf("load durable Discord progress message: %w", err)
		}
		if record != nil {
			messageID = record.MessageID
		}
	}
	if messageID != "" {
		if err := s.deleteDiscordWebhookMessage(ctx, item.channel.URL, messageID); err != nil {
			return err
		}
	}
	if err := s.repo.DeleteProgressMessage(ctx, item.channel.ID, item.recordKey); err != nil {
		return fmt.Errorf("delete Discord progress record: %w", err)
	}
	delete(s.progressMessages, key)
	return nil
}

func progressMessageCacheKey(channelID, recordKey string) string {
	return channelID + "\x00" + recordKey
}

func (s *Service) persistDiscordProgressMessage(ctx context.Context, item delivery, messageID string) error {
	err := s.repo.UpsertProgressMessage(ctx, &models.NotificationProgressMessage{
		ChannelID:   item.channel.ID,
		ProfileID:   item.event.ProfileID,
		PlaybackKey: item.recordKey,
		MessageID:   messageID,
		UpdatedAt:   time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("persist Discord progress message: %w", err)
	}
	return nil
}

func (s *Service) deleteDiscordWebhookMessage(ctx context.Context, webhookURL, messageID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		discordWebhookMessageURL(webhookURL, messageID, false), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "mediastorm-notifications/1.0")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if (resp.StatusCode < 200 || resp.StatusCode >= 300) && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("destination returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func discordWebhookMessageURL(rawURL, messageID string, wait bool) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if messageID != "" {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/messages/" + url.PathEscape(messageID)
	}
	query := parsed.Query()
	query.Del("wait")
	if wait {
		query.Set("wait", "true")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Service) withProfileName(channel models.NotificationChannel, event models.NotificationEvent) models.NotificationEvent {
	if s.profiles != nil {
		if profile, ok := s.profiles.Get(channel.ProfileID); ok {
			event.ProfileName = profile.Name
		}
	}
	return event
}

func (s *Service) withDeviceName(event models.NotificationEvent) models.NotificationEvent {
	if s.clients != nil && event.ClientID != "" {
		if client, err := s.clients.Get(event.ClientID); err == nil && client != nil {
			event.DeviceName = firstNonEmpty(client.Nickname, client.Name, client.DeviceName, client.DeviceType)
		}
	}
	return event
}

// Format renders the two safe, non-executable notification template sections.
func Format(channel models.NotificationChannel, event models.NotificationEvent) (string, string) {
	values := templateValues(event)
	title := render(channel.TitleTemplate, values)
	if channel.IncludeDeviceName && channel.Type == models.NotificationChannelDiscord &&
		strings.HasPrefix(event.Type, "watch.") && values["deviceName"] != "" &&
		!strings.Contains(channel.TitleTemplate, "{{deviceName}}") {
		title = values["deviceName"] + " - " + title
	}
	if channel.IncludeProfileName && channel.Type == models.NotificationChannelDiscord &&
		strings.HasPrefix(event.Type, "watch.") && values["profileName"] != "" &&
		!strings.Contains(channel.TitleTemplate, "{{profileName}}") {
		title = values["profileName"] + " - " + title
	}
	return title, render(channel.BodyTemplate, values)
}

func templateValues(event models.NotificationEvent) map[string]string {
	title := event.Title
	if event.MediaType == "episode" && event.SeriesTitle != "" {
		title = event.SeriesTitle
	}
	episode := ""
	if event.SeasonNumber > 0 || event.EpisodeNumber > 0 {
		episode = fmt.Sprintf("S%02dE%02d", event.SeasonNumber, event.EpisodeNumber)
		if event.EpisodeTitle != "" {
			episode += " · " + event.EpisodeTitle
		}
	}
	mediaLabel := event.MediaType
	if mediaLabel != "" {
		mediaLabel = strings.ToUpper(mediaLabel[:1]) + mediaLabel[1:]
	}
	if episode != "" {
		mediaLabel += " · " + episode
	}
	percent := ""
	progressLabel := ""
	if event.Type != models.NotificationEventWatchWatched &&
		(event.Type == models.NotificationEventWatchProgress || event.Percent > 0) {
		displayPercent := max(event.Percent, 0)
		percent = strconv.FormatFloat(displayPercent, 'f', 0, 64)
		progressLabel = " · " + percent + "%"
	}
	releaseLabel := ""
	if event.ReleaseType != "" || event.ReleaseDate != "" {
		releaseLabel = " · " + strings.TrimSpace(strings.Join(nonEmpty(releaseTypeLabel(event.ReleaseType), event.ReleaseDate), " · "))
	}
	return map[string]string{
		"event":         event.Type,
		"eventLabel":    eventLabel(event.Type),
		"profileName":   strings.TrimSpace(event.ProfileName),
		"deviceName":    strings.TrimSpace(event.DeviceName),
		"title":         title,
		"year":          optionalInt(event.Year),
		"mediaType":     event.MediaType,
		"mediaLabel":    mediaLabel,
		"seriesTitle":   event.SeriesTitle,
		"episodeTitle":  event.EpisodeTitle,
		"episode":       episode,
		"season":        optionalInt(event.SeasonNumber),
		"episodeNumber": optionalInt(event.EpisodeNumber),
		"percent":       percent,
		"progressLabel": progressLabel,
		"releaseType":   releaseTypeLabel(event.ReleaseType),
		"releaseDate":   event.ReleaseDate,
		"releaseLabel":  releaseLabel,
		"source":        event.Source,
		"rank":          optionalInt(event.SourceRank),
		"posterUrl":     event.PosterURL,
	}
}

func render(template string, values map[string]string) string {
	result := template
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = strings.ReplaceAll(result, "{{"+key+"}}", values[key])
	}
	return strings.TrimSpace(result)
}

// sameHostRedirect refuses redirects that leave the configured webhook host, so
// a destination cannot bounce deliveries to a different (e.g. internal) host.
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("stopped after 5 redirects")
	}
	if !sameRedirectHost(req.URL, via[0].URL) {
		return errors.New("webhook redirected to a different host")
	}
	return nil
}

// sameRedirectHost compares hostnames case-insensitively with default ports
// normalised, and treats discord.com and discordapp.com (and their
// subdomains) as one host.
func sameRedirectHost(a, b *url.URL) bool {
	ha, hb := strings.ToLower(a.Hostname()), strings.ToLower(b.Hostname())
	if ha != hb && !(isDiscordHost(ha) && isDiscordHost(hb)) {
		return false
	}
	// Same port, or each on its scheme's default port (http->https upgrade).
	return effectivePort(a) == effectivePort(b) || (defaultPort(a) && defaultPort(b))
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return "443"
}

func defaultPort(u *url.URL) bool {
	p := effectivePort(u)
	return (strings.EqualFold(u.Scheme, "http") && p == "80") || (strings.EqualFold(u.Scheme, "https") && p == "443")
}

func isDiscordHost(host string) bool {
	host = strings.ToLower(host)
	for _, d := range []string{"discord.com", "discordapp.com"} {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func validateDestination(kind, rawURL string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errors.New("a valid HTTP or HTTPS webhook URL is required")
	}
	if parsed.User != nil {
		return errors.New("webhook URLs cannot contain user information")
	}
	if kind == models.NotificationChannelDiscord {
		host := strings.ToLower(parsed.Hostname())
		if host != "discord.com" && host != "discordapp.com" {
			return errors.New("Discord webhooks must use discord.com")
		}
		if !strings.HasPrefix(parsed.Path, "/api/webhooks/") {
			return errors.New("invalid Discord webhook URL")
		}
	}
	return nil
}

func normalizeEvents(events []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(events))
	for _, event := range events {
		event = strings.TrimSpace(event)
		if validEvents[event] && !seen[event] {
			seen[event] = true
			result = append(result, event)
		}
	}
	sort.Strings(result)
	return result
}

func normalizeReleaseTypes(releaseTypes []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(releaseTypes))
	for _, releaseType := range releaseTypes {
		canonical := validReleaseTypes[strings.ToLower(strings.TrimSpace(releaseType))]
		if canonical != "" && !seen[canonical] {
			seen[canonical] = true
			result = append(result, canonical)
		}
	}
	sort.Strings(result)
	return result
}

func releaseTypeLabel(releaseType string) string {
	switch strings.ToLower(strings.TrimSpace(releaseType)) {
	case "digital":
		return "Digital"
	case "physical":
		return "Physical"
	case "theatrical":
		return "Theatrical"
	case "theatricallimited":
		return "Limited Theatrical"
	case "premiere":
		return "Premiere"
	case "tv":
		return "TV"
	case "availability":
		return "Available"
	default:
		return strings.TrimSpace(releaseType)
	}
}

func channelAcceptsRelease(channel models.NotificationChannel, event models.NotificationEvent) bool {
	if event.Type != models.NotificationEventRelease {
		return true
	}
	releaseTypes := channel.ReleaseTypes
	if releaseTypes == nil {
		releaseTypes = defaultReleaseTypes
	}
	if !contains(releaseTypes, event.ReleaseType) {
		return false
	}
	if event.Source == "watchlist" {
		return channel.NotifyWatchlist
	}
	if event.Source == "top-trending" || event.Source == "trending" {
		return channel.NotifyTrending && event.SourceRank > 0 && event.SourceRank <= channel.TrendingLimit
	}
	return false
}

func releaseObservationKey(item models.CalendarItem) string {
	source := item.Source
	if source == "top-trending" || source == "trending" {
		source = "trending"
	}
	return strings.Join([]string{source, item.MediaType, fallbackMediaIdentity(item.Title, item.Year),
		item.ReleaseType, item.AirDate,
		strconv.Itoa(item.SeasonNumber), strconv.Itoa(item.EpisodeNumber)}, "|")
}

func releaseEventIdentity(event models.NotificationEvent) string {
	return releaseEventIdentities(event)[0]
}

func releaseEventIdentities(event models.NotificationEvent) []string {
	identities := mediaIdentityAliases(event.ExternalIDs, event.Title, event.Year)
	result := make([]string, 0, len(identities))
	for _, identity := range identities {
		result = append(result, strings.Join([]string{
			event.MediaType, identity, event.ReleaseType, event.ReleaseDate,
			strconv.Itoa(event.SeasonNumber), strconv.Itoa(event.EpisodeNumber),
		}, "|"))
		// Also claim a short-lived media-level identity so an availability
		// status snapshot and its detailed calendar release cannot notify twice
		// during the same transition.
		result = append(result, strings.Join([]string{
			event.MediaType, identity, "available",
			strconv.Itoa(event.SeasonNumber), strconv.Itoa(event.EpisodeNumber),
		}, "|"))
	}
	return result
}

func mediaIdentityAliases(externalIDs map[string]string, title string, year int) []string {
	normalized := make(map[string]string, len(externalIDs))
	for key, value := range externalIDs {
		key = strings.ToLower(strings.TrimSpace(key))
		key = strings.TrimSuffix(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""), "id")
		if value = strings.TrimSpace(value); value != "" {
			normalized[key] = strings.ToLower(value)
		}
	}
	identities := make([]string, 0, 4)
	for _, provider := range []string{"tmdb", "tvdb", "imdb"} {
		if value := normalized[provider]; value != "" {
			identities = append(identities, provider+":"+value)
		}
	}
	return append(identities, fallbackMediaIdentity(title, year))
}

func fallbackMediaIdentity(title string, year int) string {
	return "title:" + strings.ToLower(strings.Join(strings.Fields(title), " ")) + ":" + strconv.Itoa(year)
}

func playbackTitle(update models.PlaybackProgressUpdate) string {
	if update.MediaType == "episode" {
		return firstNonEmpty(update.SeriesName, update.EpisodeName, update.ItemID)
	}
	return firstNonEmpty(update.MovieName, update.ItemID)
}

func notificationPlaybackKey(update models.PlaybackProgressUpdate) string {
	return strings.ToLower(strings.TrimSpace(update.MediaType)) + ":" +
		url.QueryEscape(strings.TrimSpace(update.ItemID))
}

func eventLabel(event string) string {
	switch event {
	case models.NotificationEventWatchStarted:
		return "Started watching"
	case models.NotificationEventWatchProgress:
		return "Watching"
	case models.NotificationEventWatchResumed:
		return "Resumed"
	case models.NotificationEventWatchWatched:
		return "Watched"
	case models.NotificationEventRelease:
		return "Now available"
	case models.NotificationEventSystemStartup:
		return "Server started"
	case models.NotificationEventSystemShutdown:
		return "Server shutting down"
	default:
		return event
	}
}

func withoutPoster(event models.NotificationEvent) models.NotificationEvent {
	event.PosterURL = ""
	return event
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func countSystemEvents(events []string) int {
	count := 0
	for _, event := range events {
		if event == models.NotificationEventSystemStartup ||
			event == models.NotificationEventSystemShutdown {
			count++
		}
	}
	return count
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func notificationReleaseArtwork(item models.CalendarItem) string {
	if item.MediaType == "episode" || item.MediaType == "series" || item.MediaType == "show" || item.MediaType == "tv" {
		candidates := []string{item.TextBackdropURL, item.BackdropURL}
		candidates = append(candidates, item.BackdropURLs...)
		candidates = append(candidates, item.TextPosterURL, item.PosterURL)
		return firstNonEmpty(candidates...)
	}
	candidates := []string{item.TextPosterURL, item.PosterURL, item.TextBackdropURL, item.BackdropURL}
	candidates = append(candidates, item.BackdropURLs...)
	return firstNonEmpty(candidates...)
}

func optionalInt(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func publicHTTPURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return parsed.String()
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
