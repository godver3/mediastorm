package recordings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"novastream/internal/datastore"
	"novastream/internal/liveusage"
	"novastream/models"
)

var (
	ErrUserIDRequired      = errors.New("user id is required")
	ErrRecordingNotFound   = errors.New("recording not found")
	ErrProfileRequired     = errors.New("profile id is required")
	ErrChannelIDRequired   = errors.New("channel id is required")
	ErrChannelNameRequired = errors.New("channel name is required")
	ErrSourceURLRequired   = errors.New("source url is required")
	ErrStartRequired       = errors.New("start time is required")
	ErrEndRequired         = errors.New("end time is required")
	ErrInvalidTimeWindow   = errors.New("end time must be after start time")
	ErrPaddingOutOfRange   = errors.New("padding must be between 0 and 3600 seconds")
	ErrRecordingTooLong    = errors.New("recording window must not exceed 24 hours")
	ErrCannotDeleteActive  = errors.New("cannot delete active recording")
)

const pollInterval = 15 * time.Second

const (
	// maxRecordingPaddingSeconds matches the cap applied to recording rules.
	maxRecordingPaddingSeconds = 3600
	// maxRecordingWindow bounds a directly created recording's start-to-stop span.
	maxRecordingWindow = 24 * time.Hour
	// maxOutputFilenameBytes keeps recording file names well under the 255-byte limit.
	maxOutputFilenameBytes = 200
)

// pausedRuleError marks airings parked by a paused rule (the datastore sets
// it and revives such rows when the rule resumes).
const pausedRuleError = "recording rule paused"

var (
	recordingRetryDelay = 2 * time.Second
	// recordingReadTimeout fails a stalled source read so the retry loop can
	// reconnect instead of ffmpeg hanging until the stop time.
	recordingReadTimeout = 15 * time.Second
	// recordingStopGrace is how long ffmpeg gets to exit after SIGINT before
	// it is killed.
	recordingStopGrace = 10 * time.Second
	execCommandContext = exec.CommandContext
)

type Service struct {
	repo           datastore.RecordingRepository
	rules          datastore.RecordingRuleRepository
	epg            ScheduleProvider
	channels       ChannelProvider
	epgoffset      func(string) time.Duration
	ffmpegPath     string
	outputDir      string
	remuxRecording func(string) error

	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	running  bool
	wg       sync.WaitGroup
	active   map[string]context.CancelFunc
	ruleWake chan struct{}
	ruleMu   sync.Mutex

	// Commercial detection (comskip.go).
	comskipConfig   func() ComskipConfig
	comskipCached   ComskipConfig
	comskipCachedAt time.Time
	comskipQueue    chan string
	comskipQueued   map[string]bool    // in comskipQueue
	comskipFailures map[string]int     // failed analyses per recording since start
	comskipMissing  string             // the missing binary last reported
	comskipRunning  string             // the recording being analysed
	comskipStop     context.CancelFunc // stops that analysis

	// rowLocks serialise the changes to one recording: Cancel and Delete
	// against the recording goroutine's status changes and finalisation,
	// which would otherwise write back a stale copy (a cancelled recording
	// marked running again, a deleted file and path restored).
	rowMu    sync.Mutex
	rowLocks map[string]*rowLock

	// contents: recording ID -> *recordedContent, for running recordings that looked back in
	// their source's window (backfill.go).
	contents sync.Map
}

type rowLock struct {
	mu   sync.Mutex
	refs int
}

// lockRecording locks one recording's row changes; call the returned func
// to unlock.
func (s *Service) lockRecording(id string) func() {
	s.rowMu.Lock()
	if s.rowLocks == nil {
		s.rowLocks = make(map[string]*rowLock)
	}
	l := s.rowLocks[id]
	if l == nil {
		l = &rowLock{}
		s.rowLocks[id] = l
	}
	l.refs++
	s.rowMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.rowMu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.rowLocks, id)
		}
		s.rowMu.Unlock()
	}
}

// statusIs tells whether the stored recording still has the status (the
// caller holds its row lock).
func (s *Service) statusIs(id string, status models.RecordingStatus) bool {
	latest, err := s.Get(id)
	return err == nil && latest.Status == status
}

func NewService(repo datastore.RecordingRepository, ffmpegPath, outputDir string) *Service {
	ffmpegPath = strings.TrimSpace(ffmpegPath)
	if strings.TrimSpace(outputDir) == "" {
		outputDir = filepath.Join("cache", "recordings")
	}
	return &Service{
		repo:           repo,
		ffmpegPath:     ffmpegPath,
		outputDir:      outputDir,
		active:         make(map[string]context.CancelFunc),
		ruleWake:       make(chan struct{}, 1),
		remuxRecording: func(path string) error { return remuxRecordingTimestamps(ffmpegPath, path) },
	}
}

type ScheduleProvider interface {
	GetScheduleMultiple(channelIDs []string, start, end time.Time) map[string][]models.EPGProgram
}

type Channel struct {
	ID        string
	TvgID     string
	Name      string
	SourceURL string
}

type ChannelProvider interface {
	RecordingChannels(ctx context.Context, profileID string) ([]Channel, error)
}

func (s *Service) SetRuleRepository(repo datastore.RecordingRuleRepository) {
	s.rules = repo
}

func (s *Service) SetRuleProviders(epg ScheduleProvider, channels ChannelProvider, offset func(string) time.Duration) {
	s.epg = epg
	s.channels = channels
	s.epgoffset = offset
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	if err := os.MkdirAll(s.outputDir, 0o755); err != nil {
		return fmt.Errorf("create recordings dir: %w", err)
	}
	if _, err := s.repo.MarkStaleActiveAsFailed(context.Background(), time.Now().UTC()); err != nil {
		log.Printf("[recordings] failed to reconcile stale recordings: %v", err)
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.running = true
	s.wg.Add(1)
	go s.loop()
	if s.rules != nil {
		s.wg.Add(1)
		go s.ruleLoop()
	}
	if s.comskipQueue != nil {
		s.wg.Add(1)
		go s.comskipLoop(s.ctx)
	}
	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.cancel()
	s.running = false
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	s.runDueRecordings()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.runDueRecordings()
		}
	}
}

func (s *Service) ruleLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	s.expandEnabledRules()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.ruleWake:
			s.expandEnabledRules()
		case <-ticker.C:
			s.expandEnabledRules()
		}
	}
}

func (s *Service) List(filter models.RecordingListFilter) ([]models.Recording, error) {
	return s.repo.List(context.Background(), filter)
}

func (s *Service) Get(id string) (*models.Recording, error) {
	recording, err := s.repo.Get(context.Background(), strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if recording == nil {
		return nil, ErrRecordingNotFound
	}
	return recording, nil
}

func (s *Service) CreateFromEPG(req models.CreateEPGRecordingRequest) (models.Recording, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.ChannelName)
	}
	return s.createRecording(models.RecordingTypeEPG, req.ProfileID, req.ChannelID, req.TvgID, req.ChannelName, title, req.Description, req.SourceURL, req.Start, req.Stop, req.PaddingBeforeSeconds, req.PaddingAfterSeconds)
}

func (s *Service) CreateTimeBlock(req models.CreateTimeBlockRecordingRequest) (models.Recording, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.ChannelName)
	}
	return s.createRecording(models.RecordingTypeTimeBlock, req.ProfileID, req.ChannelID, req.TvgID, req.ChannelName, title, req.Description, req.SourceURL, req.Start, req.Stop, req.PaddingBeforeSeconds, req.PaddingAfterSeconds)
}

func (s *Service) createRecording(kind models.RecordingType, userID, channelID, tvgID, channelName, title, description, sourceURL, startRaw, endRaw string, padBefore, padAfter int) (models.Recording, error) {
	userID = strings.TrimSpace(userID)
	channelID = strings.TrimSpace(channelID)
	channelName = strings.TrimSpace(channelName)
	sourceURL = strings.TrimSpace(sourceURL)
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	if userID == "" {
		return models.Recording{}, ErrProfileRequired
	}
	if channelID == "" {
		return models.Recording{}, ErrChannelIDRequired
	}
	if channelName == "" {
		return models.Recording{}, ErrChannelNameRequired
	}
	if sourceURL == "" {
		return models.Recording{}, ErrSourceURLRequired
	}
	if strings.TrimSpace(startRaw) == "" {
		return models.Recording{}, ErrStartRequired
	}
	if strings.TrimSpace(endRaw) == "" {
		return models.Recording{}, ErrEndRequired
	}
	startAt, err := time.Parse(time.RFC3339, startRaw)
	if err != nil {
		return models.Recording{}, fmt.Errorf("parse start time: %w", err)
	}
	endAt, err := time.Parse(time.RFC3339, endRaw)
	if err != nil {
		return models.Recording{}, fmt.Errorf("parse end time: %w", err)
	}
	if !endAt.After(startAt) {
		return models.Recording{}, ErrInvalidTimeWindow
	}
	if endAt.Sub(startAt) > maxRecordingWindow {
		return models.Recording{}, ErrRecordingTooLong
	}
	if padBefore > maxRecordingPaddingSeconds || padAfter > maxRecordingPaddingSeconds {
		return models.Recording{}, ErrPaddingOutOfRange
	}
	now := time.Now().UTC()
	recording := models.Recording{
		ID:                   uuid.NewString(),
		UserID:               userID,
		Type:                 kind,
		Status:               models.RecordingStatusPending,
		ChannelID:            channelID,
		TvgID:                strings.TrimSpace(tvgID),
		ChannelName:          channelName,
		Title:                title,
		Description:          description,
		SourceURL:            sourceURL,
		StartAt:              startAt.UTC(),
		EndAt:                endAt.UTC(),
		PaddingBeforeSeconds: max(0, padBefore),
		PaddingAfterSeconds:  max(0, padAfter),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if recording.Title == "" {
		recording.Title = recording.ChannelName
	}
	if kind == models.RecordingTypeEPG {
		recording.Guide = s.guideFor(userID, recording.TvgID, recording.StartAt, recording.Title)
	}
	if err := s.authorizeSource(&recording); err != nil {
		return models.Recording{}, err
	}
	if err := s.repo.Create(context.Background(), &recording); err != nil {
		return models.Recording{}, err
	}
	s.maybeStartRecordingImmediately(recording)
	return recording, nil
}

func (s *Service) maybeStartRecordingImmediately(recording models.Recording) {
	s.mu.Lock()
	running := s.running
	_, active := s.active[recording.ID]
	ctxReady := s.ctx != nil
	s.mu.Unlock()
	if !running || active || !ctxReady {
		return
	}

	startAt := recording.StartAt.Add(-time.Duration(recording.PaddingBeforeSeconds) * time.Second)
	if startAt.After(time.Now().UTC()) {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.startRecording(recording)
	}()
}

func (s *Service) Cancel(id string) error {
	defer s.lockRecording(id)()
	recording, err := s.Get(id)
	if err != nil {
		return err
	}
	return s.cancelLocked(recording)
}

func (s *Service) cancelLocked(recording *models.Recording) error {
	now := time.Now().UTC()
	switch recording.Status {
	case models.RecordingStatusCompleted, models.RecordingStatusFailed, models.RecordingStatusCancelled:
		return nil
	case models.RecordingStatusRunning, models.RecordingStatusStarting:
		recording.Status = models.RecordingStatusCancelled
		recording.ActualEndAt = &now
		recording.UpdatedAt = now
		recording.Error = ""
		if err := s.repo.Update(context.Background(), recording); err != nil {
			return err
		}
		s.mu.Lock()
		cancel := s.active[recording.ID]
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil
	default:
		recording.Status = models.RecordingStatusCancelled
		recording.ActualEndAt = &now
		recording.UpdatedAt = now
		recording.Error = ""
		return s.repo.Update(context.Background(), recording)
	}
}

func (s *Service) Delete(id string) error {
	s.cancelComskip(id)
	// Locked: a cancelled recording's goroutine may still be finalising it.
	defer s.lockRecording(id)()
	recording, err := s.Get(id)
	if err != nil {
		return err
	}
	if recording.Status == models.RecordingStatusPending && strings.TrimSpace(recording.RuleID) != "" {
		// Keep the schedule key as a tombstone so the rule expander does not
		// recreate a single occurrence the user removed from the queue.
		return s.cancelLocked(recording)
	}
	if recording.Status == models.RecordingStatusRunning || recording.Status == models.RecordingStatusStarting {
		return ErrCannotDeleteActive
	}
	if strings.TrimSpace(recording.RuleID) != "" && recording.EndAt.After(time.Now()) {
		// A rule's airing that is still on (stopped early, then deleted)
		// would be scheduled again by the rule: keep the row as the same
		// tombstone as above, without its file.
		if strings.TrimSpace(recording.OutputPath) != "" {
			if err := os.Remove(recording.OutputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("delete recording file: %w", err)
			}
			if err := removeEDL(recording.OutputPath); err != nil {
				return err
			}
		}
		recording.Status = models.RecordingStatusCancelled
		recording.OutputPath = ""
		recording.OutputSizeBytes = 0
		if recording.Error == pausedRuleError {
			// A paused airing comes back when its rule resumes; a deleted
			// one stays skipped.
			recording.Error = ""
		}
		recording.UpdatedAt = time.Now().UTC()
		return s.repo.Update(context.Background(), recording)
	}
	if strings.TrimSpace(recording.OutputPath) != "" {
		if err := os.Remove(recording.OutputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete recording file: %w", err)
		}
		if err := removeEDL(recording.OutputPath); err != nil {
			return err
		}
	}
	return s.repo.Delete(context.Background(), recording.ID)
}

func (s *Service) ListRules(userID string, includeAll bool) ([]models.RecordingRule, error) {
	if s.rules == nil {
		return []models.RecordingRule{}, nil
	}
	return s.rules.ListRules(context.Background(), userID, includeAll, false)
}

func (s *Service) GetRule(id string) (*models.RecordingRule, error) {
	if s.rules == nil {
		return nil, errors.New("recording rules are unavailable")
	}
	return s.rules.GetRule(context.Background(), strings.TrimSpace(id))
}

func (s *Service) CreateRule(rule models.RecordingRule) (models.RecordingRule, error) {
	if s.rules == nil {
		return models.RecordingRule{}, errors.New("recording rules are unavailable")
	}
	rule.UserID = strings.TrimSpace(rule.UserID)
	rule.Pattern = strings.TrimSpace(rule.Pattern)
	rule.ChannelID = strings.TrimSpace(rule.ChannelID)
	rule.TvgID = strings.TrimSpace(rule.TvgID)
	rule.ChannelName = strings.TrimSpace(rule.ChannelName)
	if rule.UserID == "" {
		return models.RecordingRule{}, ErrProfileRequired
	}
	if rule.Pattern == "" || len(rule.Pattern) > 512 {
		return models.RecordingRule{}, errors.New("rule pattern must contain 1 to 512 characters")
	}
	if rule.PaddingBeforeSeconds < 0 || rule.PaddingBeforeSeconds > 3600 || rule.PaddingAfterSeconds < 0 || rule.PaddingAfterSeconds > 3600 {
		return models.RecordingRule{}, errors.New("recording buffers must be between 0 and 60 minutes")
	}
	switch rule.MatchType {
	case models.RecordingRuleMatchTitle:
		if rule.TvgID == "" || rule.ChannelID == "" {
			return models.RecordingRule{}, errors.New("title rules must be attached to a channel with EPG data")
		}
		rule.AllChannels = false
	case models.RecordingRuleMatchRegex:
		if _, err := compileRecordingRegex(rule.Pattern); err != nil {
			return models.RecordingRule{}, fmt.Errorf("invalid regular expression: %w", err)
		}
		if !rule.AllChannels && rule.TvgID == "" {
			return models.RecordingRule{}, errors.New("select a channel or choose all channels")
		}
	default:
		return models.RecordingRule{}, errors.New("match type must be title or regex")
	}
	existingRules, err := s.rules.ListRules(context.Background(), rule.UserID, false, false)
	if err != nil {
		return models.RecordingRule{}, err
	}
	for _, existing := range existingRules {
		if existing.MatchType != rule.MatchType {
			continue
		}
		samePattern := existing.Pattern == rule.Pattern
		if rule.MatchType == models.RecordingRuleMatchTitle {
			samePattern = normalizeRecordingTitle(existing.Pattern) == normalizeRecordingTitle(rule.Pattern)
		}
		sameScope := existing.AllChannels == rule.AllChannels && (rule.AllChannels ||
			(existing.ChannelID != "" && strings.EqualFold(existing.ChannelID, rule.ChannelID)) ||
			(existing.TvgID != "" && strings.EqualFold(existing.TvgID, rule.TvgID)))
		if samePattern && sameScope {
			// Re-creating a rule with the other "new episodes only" choice
			// switches the existing rule rather than failing.
			if existing.NewOnly != rule.NewOnly {
				newOnly := rule.NewOnly
				return s.UpdateRule(existing.ID, models.UpdateRecordingRuleRequest{NewOnly: &newOnly})
			}
			return models.RecordingRule{}, errors.New("an equivalent recording rule already exists")
		}
	}
	rule.ID = uuid.NewString()
	rule.Enabled = true
	rule.CreatedAt = time.Now().UTC()
	rule.UpdatedAt = rule.CreatedAt
	if err := s.rules.CreateRule(context.Background(), &rule); err != nil {
		return models.RecordingRule{}, err
	}
	s.wakeRuleExpansion()
	return rule, nil
}

func (s *Service) UpdateRule(id string, update models.UpdateRecordingRuleRequest) (models.RecordingRule, error) {
	if s.rules == nil {
		return models.RecordingRule{}, errors.New("recording rules are unavailable")
	}
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	rule, err := s.rules.GetRule(context.Background(), strings.TrimSpace(id))
	if err != nil {
		return models.RecordingRule{}, err
	}
	if rule == nil {
		return models.RecordingRule{}, ErrRecordingNotFound
	}
	wasEnabled := rule.Enabled
	wasNewOnly := rule.NewOnly
	if update.Enabled != nil {
		rule.Enabled = *update.Enabled
	}
	if update.NewOnly != nil {
		rule.NewOnly = *update.NewOnly
	}
	if update.PaddingBeforeSeconds != nil {
		rule.PaddingBeforeSeconds = *update.PaddingBeforeSeconds
	}
	if update.PaddingAfterSeconds != nil {
		rule.PaddingAfterSeconds = *update.PaddingAfterSeconds
	}
	if rule.PaddingBeforeSeconds < 0 || rule.PaddingBeforeSeconds > 3600 || rule.PaddingAfterSeconds < 0 || rule.PaddingAfterSeconds > 3600 {
		return models.RecordingRule{}, errors.New("recording buffers must be between 0 and 60 minutes")
	}
	rule.UpdatedAt = time.Now().UTC()
	if err := s.rules.UpdateRule(context.Background(), rule); err != nil {
		return models.RecordingRule{}, err
	}
	// Changing the filter (or pausing) parks the pending airings as "paused";
	// the next expansion pass revives the ones the rule still matches.
	if (wasEnabled && !rule.Enabled) || (rule.Enabled && wasNewOnly != rule.NewOnly) {
		if err := s.rules.PausePendingRuleRecordings(context.Background(), rule.ID, time.Now().UTC()); err != nil {
			return models.RecordingRule{}, err
		}
	}
	if update.PaddingBeforeSeconds != nil || update.PaddingAfterSeconds != nil {
		if err := s.rules.UpdatePendingRuleRecordingBuffers(context.Background(), rule.ID, rule.PaddingBeforeSeconds, rule.PaddingAfterSeconds); err != nil {
			return models.RecordingRule{}, err
		}
	}
	s.wakeRuleExpansionAfterCurrentPass()
	return *rule, nil
}

func (s *Service) DeleteRule(id string) error {
	if s.rules == nil {
		return errors.New("recording rules are unavailable")
	}
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	rule, err := s.rules.GetRule(context.Background(), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if rule == nil {
		return ErrRecordingNotFound
	}
	now := time.Now().UTC()
	if err := s.rules.CancelPendingRuleRecordings(context.Background(), rule.ID, now); err != nil {
		return err
	}
	return s.rules.DeleteRule(context.Background(), rule.ID)
}

func (s *Service) wakeRuleExpansion() {
	select {
	case s.ruleWake <- struct{}{}:
	default:
	}
}

func (s *Service) wakeRuleExpansionAfterCurrentPass() {
	go func() {
		s.ruleMu.Lock()
		s.ruleMu.Unlock()
		s.wakeRuleExpansion()
	}()
}

func (s *Service) expandEnabledRules() {
	if s.rules == nil || s.epg == nil || s.channels == nil {
		return
	}
	if !s.ruleMu.TryLock() {
		return
	}
	defer s.ruleMu.Unlock()

	rules, err := s.rules.ListRules(context.Background(), "", true, true)
	if err != nil {
		log.Printf("[recordings] list enabled rules failed: %v", err)
		return
	}
	if len(rules) == 0 {
		return
	}
	byProfile := make(map[string][]models.RecordingRule)
	for _, rule := range rules {
		byProfile[rule.UserID] = append(byProfile[rule.UserID], rule)
	}
	for profileID, profileRules := range byProfile {
		ctx := context.Background()
		if s.ctx != nil {
			ctx = s.ctx
		}
		channels, err := s.channels.RecordingChannels(ctx, profileID)
		if err != nil {
			log.Printf("[recordings] resolve channels for recurring rules failed profile=%s: %v", profileID, err)
			continue
		}
		channelByTVG := make(map[string]Channel, len(channels))
		var epgIDs []string
		seen := make(map[string]bool)
		for _, channel := range channels {
			tvgID := strings.TrimSpace(channel.TvgID)
			if tvgID == "" {
				continue
			}
			channelByTVG[strings.ToLower(tvgID)] = channel
			if !seen[strings.ToLower(tvgID)] {
				seen[strings.ToLower(tvgID)] = true
				epgIDs = append(epgIDs, tvgID)
			}
		}
		if len(epgIDs) == 0 {
			continue
		}
		offset := time.Duration(0)
		if s.epgoffset != nil {
			offset = s.epgoffset(profileID)
		}
		now := time.Now().UTC()
		horizonEnd := now.Add(7 * 24 * time.Hour)
		schedules := s.epg.GetScheduleMultiple(epgIDs, now.Add(-offset), horizonEnd.Add(-offset))
		taken, err := s.takenAirings(ctx, profileID)
		if err != nil {
			// Without the list every airing looks free and would be scheduled twice.
			log.Printf("[recordings] skipping rule expansion for profile=%s: %v", profileID, err)
			continue
		}
		for _, rule := range profileRules {
			var compiled *regexp.Regexp
			if rule.MatchType == models.RecordingRuleMatchRegex {
				compiled, err = compileRecordingRegex(rule.Pattern)
				if err != nil {
					log.Printf("[recordings] skipping invalid stored regex rule=%s: %v", rule.ID, err)
					continue
				}
			}
			for epgID, programs := range schedules {
				channel, ok := channelByTVG[strings.ToLower(epgID)]
				if !ok || channel.SourceURL == "" || !ruleMatchesChannel(rule, channel) {
					continue
				}
				guideFlagsNew := rule.NewOnly && guideMarksNewAirings(programs)
				for _, program := range programs {
					if rule.NewOnly && !isNewAiring(program, guideFlagsNew) {
						continue
					}
					program.Start = program.Start.Add(offset)
					program.Stop = program.Stop.Add(offset)
					if !program.Stop.After(now) || !program.Start.Before(horizonEnd) || !matchesRecordingRule(rule, compiled, program.Title) {
						continue
					}
					// The airing may already be recorded: a single recording of
					// it, or another rule's airing.
					if taken.covers(channel, program.Start, rule.ID) {
						continue
					}
					scheduleKey := strings.ToLower(epgID) + ":" + program.Start.UTC().Format(time.RFC3339Nano)
					job := models.Recording{
						ID:                   uuid.NewString(),
						UserID:               profileID,
						RuleID:               rule.ID,
						ScheduleKey:          scheduleKey,
						Type:                 models.RecordingTypeEPG,
						Status:               models.RecordingStatusPending,
						ChannelID:            channel.ID,
						TvgID:                channel.TvgID,
						ChannelName:          channel.Name,
						Title:                program.Title,
						Description:          program.Description,
						Guide:                guideFromProgram(program),
						SourceURL:            channel.SourceURL,
						StartAt:              program.Start.UTC(),
						EndAt:                program.Stop.UTC(),
						PaddingBeforeSeconds: rule.PaddingBeforeSeconds,
						PaddingAfterSeconds:  rule.PaddingAfterSeconds,
						CreatedAt:            now,
						UpdatedAt:            now,
					}
					if created, err := s.rules.CreateScheduledRecordingIfAbsent(ctx, &job); err != nil {
						log.Printf("[recordings] materialize rule=%s channel=%s failed: %v", rule.ID, channel.ID, err)
					} else if created {
						taken.add(job)
					}
				}
			}
		}
	}
}

// guideMarksNewAirings reports whether a channel's guide flags new airings
// explicitly. When it does, an unflagged airing is treated as a rerun.
func guideMarksNewAirings(programs []models.EPGProgram) bool {
	for _, program := range programs {
		if program.New {
			return true
		}
	}
	return false
}

// isNewAiring decides whether a "new episodes only" rule records an airing.
// Guides flag either new airings (<new/>, <premiere>, <live/>) or reruns
// (<previously-shown>), sometimes both. A guide with no flags at all gives no
// way to tell, so its airings are all recorded, as before the option existed.
func isNewAiring(program models.EPGProgram, guideFlagsNew bool) bool {
	if program.New {
		return true
	}
	if program.PreviouslyShown {
		return false
	}
	return !guideFlagsNew
}

func ruleMatchesChannel(rule models.RecordingRule, channel Channel) bool {
	if rule.AllChannels {
		return true
	}
	return (rule.ChannelID != "" && strings.EqualFold(rule.ChannelID, channel.ID)) ||
		(rule.TvgID != "" && strings.EqualFold(rule.TvgID, channel.TvgID))
}

func compileRecordingRegex(pattern string) (*regexp.Regexp, error) {
	// EPG capitalization varies by provider. An explicit (?-i) in the pattern
	// still lets the user request case-sensitive matching.
	return regexp.Compile("(?i)" + pattern)
}

func matchesRecordingRule(rule models.RecordingRule, compiled *regexp.Regexp, title string) bool {
	switch rule.MatchType {
	case models.RecordingRuleMatchTitle:
		return normalizeRecordingTitle(rule.Pattern) == normalizeRecordingTitle(title)
	case models.RecordingRuleMatchRegex:
		return compiled != nil && compiled.MatchString(title)
	default:
		return false
	}
}

// RuleMatchesTitle reports whether a rule's pattern matches a programme title
// the way scheduling matches it (normalised titles, case-insensitive regex).
// Channel and new-airing conditions are not checked.
func RuleMatchesTitle(rule models.RecordingRule, title string) bool {
	var compiled *regexp.Regexp
	if rule.MatchType == models.RecordingRuleMatchRegex {
		c, err := compileRecordingRegex(rule.Pattern)
		if err != nil {
			return false
		}
		compiled = c
	}
	return matchesRecordingRule(rule, compiled, title)
}

func normalizeRecordingTitle(value string) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
		} else if normalized.Len() > 0 && !strings.HasSuffix(normalized.String(), " ") {
			normalized.WriteByte(' ')
		}
	}
	return strings.TrimSpace(normalized.String())
}

func (s *Service) runDueRecordings() {
	now := time.Now().UTC()
	due, err := s.repo.List(context.Background(), models.RecordingListFilter{
		Statuses:        []models.RecordingStatus{models.RecordingStatusPending},
		IncludeAll:      true,
		OnlyStartBefore: &now,
		Limit:           20,
	})
	if err != nil {
		log.Printf("[recordings] list due recordings failed: %v", err)
		return
	}
	for _, recording := range due {
		s.mu.Lock()
		_, active := s.active[recording.ID]
		s.mu.Unlock()
		if active {
			continue
		}
		rec := recording
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.startRecording(rec)
		}()
	}
}

func (s *Service) startRecording(recording models.Recording) {
	now := time.Now().UTC()
	stopAt := recording.EndAt.Add(time.Duration(recording.PaddingAfterSeconds) * time.Second)
	unlock := s.lockRecording(recording.ID)
	if !s.statusIs(recording.ID, models.RecordingStatusPending) {
		// Cancelled, deleted or already started since it was listed.
		unlock()
		return
	}
	if !stopAt.After(now) {
		recording.Status = models.RecordingStatusFailed
		recording.Error = "recording window expired before start"
		recording.ActualEndAt = &now
		recording.UpdatedAt = now
		if err := s.repo.Update(context.Background(), &recording); err != nil {
			log.Printf("[recordings] mark expired recording failed: %v", err)
		}
		unlock()
		return
	}
	if err := s.authorizeSource(&recording); err != nil {
		s.finalizeFailure(recording, now, err.Error())
		unlock()
		return
	}
	recording.Status = models.RecordingStatusStarting
	recording.OutputPath = s.buildOutputPath(recording)
	recording.UpdatedAt = now
	err := s.repo.Update(context.Background(), &recording)
	unlock()
	if err != nil {
		log.Printf("[recordings] update starting status failed: %v", err)
		return
	}
	failStarting := func(msg string) {
		defer s.lockRecording(recording.ID)()
		if s.statusIs(recording.ID, models.RecordingStatusStarting) {
			s.finalizeFailure(recording, now, msg)
		}
	}
	if strings.TrimSpace(s.ffmpegPath) == "" {
		failStarting("ffmpeg is not configured")
		return
	}
	if err := os.MkdirAll(filepath.Dir(recording.OutputPath), 0o755); err != nil {
		failStarting(fmt.Sprintf("create output directory: %v", err))
		return
	}

	ctx, cancel := context.WithCancel(s.ctx)
	recording.Status = models.RecordingStatusRunning
	recording.ActualStartAt = &now
	recording.UpdatedAt = now
	unlock = s.lockRecording(recording.ID)
	if !s.statusIs(recording.ID, models.RecordingStatusStarting) {
		// Cancelled (or deleted) while starting.
		unlock()
		cancel()
		return
	}
	if err := s.repo.Update(context.Background(), &recording); err != nil {
		unlock()
		cancel()
		log.Printf("[recordings] update running status failed: %v", err)
		return
	}
	s.mu.Lock()
	s.active[recording.ID] = cancel
	s.mu.Unlock()
	unlock()
	liveusage.GetTracker().StartRecording(recording.ID, recording.UserID)
	defer func() {
		s.mu.Lock()
		delete(s.active, recording.ID)
		s.mu.Unlock()
		liveusage.GetTracker().EndRecording(recording.ID)
		cancel()
	}()

	log.Printf("[recordings] recording start id=%s title=%q channel=%q source=%s window=%s..%s stopAt=%s",
		recording.ID, recording.Title, recording.ChannelName, redactSourceURL(recording.SourceURL),
		recording.StartAt.Format(time.RFC3339), recording.EndAt.Format(time.RFC3339), stopAt.Format(time.RFC3339))

	attempt := 0
	lastErrMsg := ""
	// The streams to record (variant.go). Attempts append to one file, whose stream layout must
	// not change: once something is recorded, a retry keeps the chosen variant.
	var streams recordingStreamChoice
	var recorded int64
	// Where the recording should begin; a late one looks back in the source's window (backfill.go).
	// Its content is then followed (-progress): a backfilled attempt that ends before its content
	// reaches the stop time is continued from there while the window still has it, until deadline.
	wantStart := recording.StartAt.Add(-time.Duration(recording.PaddingBeforeSeconds) * time.Second)
	// Only a recording whose first attempt starts late looks back: an on-time one whose first
	// attempts fail must not turn "late" after recordingBackfillMinLate and fetch the window.
	lateAtStart := time.Since(wantStart) >= recordingBackfillMinLate
	deadline := stopAt
	lookedBack, backfillPlanned := false, false
	var contentStart, resumeAt, lastResume time.Time
	var content *recordedContent
	resumeFailures := 0
	defer s.forgetContent(recording.ID)
	// endOf is where the recording's content ends by ts: for one followed, its content start plus
	// what it holds, which its backdated start counts from.
	endOf := func(ts time.Time) time.Time {
		if content != nil && !contentStart.IsZero() {
			if total := content.total(); total > 0 {
				return contentStart.Add(total)
			}
		}
		return ts
	}
	for {
		resuming := !resumeAt.IsZero()
		if time.Until(stopAt) <= 0 && (!resuming || time.Until(deadline) <= 0) {
			break
		}
		attempt++

		lookupCtx, cancelLookup := context.WithDeadline(ctx, deadline)
		streams = chooseRecordingStreams(lookupCtx, recording.SourceURL, streams, recorded > 0)
		var backfill recordingBackfill
		var planErr error
		switch {
		case resuming:
			// No lead on a resume: starting earlier than where the content ended would append
			// those segments a second time.
			backfill, planErr = planRecordingBackfillLead(lookupCtx, recording.SourceURL, streams, resumeAt, 0)
		case recorded == 0 && !backfillPlanned && lateAtStart:
			// Once: a backfilled start that records nothing is not tried again (a window that did
			// not load is asked again on the next attempt).
			backfill, planErr = planRecordingBackfill(lookupCtx, recording.SourceURL, streams, wantStart)
			backfillPlanned = planErr == nil && time.Since(wantStart) >= recordingBackfillMinLate
			planErr = nil
		}
		cancelLookup()
		if resuming && planErr != nil && ctx.Err() == nil && resumeFailures < recordingBackfillResumeTries {
			// The source's playlist did not load: ask again (the window keeps the content for
			// hours) rather than going live and leaving the rest out, a few times.
			resumeFailures++
			log.Printf("[recordings] recording id=%s: the source's window did not load (%v), asking again", recording.ID, planErr)
			if !s.waitBeforeRetry(ctx) {
				s.handleInterruptedRecording(recording.ID, endOf(time.Now().UTC()))
				return
			}
			continue
		}
		if backfill.active() {
			resumeFailures = 0
		}
		resumeAt = time.Time{}
		if time.Until(stopAt) <= 0 && !backfill.active() {
			break
		}
		attemptStreams := streams
		if backfill.active() {
			if content == nil {
				content = s.followContent(recording.ID)
				deadline = backfill.deadline(stopAt)
			} else if d := backfill.deadline(stopAt); d.After(deadline) {
				// A resume planned from a later point is further behind: allow it the time it needs.
				deadline = d
			}
			if streams.variant == "" && !streams.pictureAndSound {
				// Everything (-map 0) may hold a stream without packets (subtitles, data), which
				// holds FFmpeg's progress back: a recording that looks back takes picture and sound.
				attemptStreams = pictureAndSoundStreams()
			}
			if resuming {
				log.Printf("[recordings] recording id=%s continues from %s in the source's window",
					recording.ID, backfill.from.UTC().Format(time.RFC3339))
			}
		}
		if !resuming && recorded == 0 && (backfill.active() || lookedBack) {
			// The recording's content begins where it starts in the source, which its duration
			// and stills count from.
			contentStart = time.Now().UTC()
			if backfill.active() {
				contentStart = backfill.from.UTC()
				log.Printf("[recordings] recording id=%s starts %s late: taking it from %s in the source's window",
					recording.ID, time.Since(wantStart).Round(time.Second), contentStart.Format(time.RFC3339))
			}
			if lookedBack && !backfill.active() {
				// The backfilled start recorded nothing: live from here, as before.
				content, deadline = nil, stopAt
				s.forgetContent(recording.ID)
			}
			lookedBack = backfill.active()
			s.setContentStart(recording.ID, contentStart)
		}
		attemptDeadline := stopAt
		if backfill.active() {
			attemptDeadline = deadline
		}
		result := s.runRecordingAttempt(ctx, recording, stopAt, attemptDeadline, attempt, attemptStreams, backfill, content)
		recorded += result.written
		if result.written > 0 {
			// The file has this layout now.
			streams = attemptStreams
		}
		if backfill.active() && !result.stoppedAtSchedule && !result.jumped && result.written > 0 && result.content > 0 {
			// Ended before its content reached the stop time: the next attempt carries on there,
			// unless this one got no further than the last (then it carries on live).
			if next := backfill.from.Add(result.content); next.After(lastResume) {
				resumeAt, lastResume = next, next
			}
		}
		streams = afterEmptyVariant(streams, result.mapsMatchedNothing, recorded)
		finishedAt := time.Now().UTC()
		if ctx.Err() != nil {
			s.handleInterruptedRecording(recording.ID, endOf(finishedAt))
			return
		}
		if result.stoppedAtSchedule {
			// Wall-clock stop reached: this attempt finished the recording,
			// unless it was a last-second retry that captured nothing.
			// A late attempt cut off by its deadline before its content reached the stop time
			// carries an error: the recording is missing its end.
			if result.written > 0 {
				lastErrMsg = result.errMsg
			}
			break
		}
		if result.errMsg != "" {
			lastErrMsg = result.errMsg
		} else {
			lastErrMsg = "source stream ended before scheduled stop time"
		}
		if time.Until(stopAt) <= 0 && (resumeAt.IsZero() || time.Until(deadline) <= 0) {
			break
		}

		if !s.waitBeforeRetry(ctx) {
			s.handleInterruptedRecording(recording.ID, endOf(time.Now().UTC()))
			return
		}
	}

	defer s.lockRecording(recording.ID)()
	latest, err := s.Get(recording.ID)
	if err != nil {
		log.Printf("[recordings] reload recording after ffmpeg exit failed: %v", err)
		return
	}
	finishedAt := time.Now().UTC()
	if latest.Status == models.RecordingStatusCancelled {
		keepContentEnd(latest, endOf(finishedAt))
		s.finalizeCancelledRecording(latest)
		return
	}
	// Retry outputs reset timestamps even when the final attempt failed.
	if attempt > 1 {
		lastErrMsg = s.repairRecordingOutput(latest, lastErrMsg)
	}

	if info, err := os.Stat(latest.OutputPath); err == nil {
		latest.OutputSizeBytes = info.Size()
	}
	// A followed recording holds this much from its content start (FFmpeg got to the stop time
	// later, and a gap holds nothing): durations count from ActualStartAt.
	finishedAt = endOf(finishedAt)
	latest.ActualEndAt = &finishedAt
	latest.UpdatedAt = finishedAt
	if latest.OutputSizeBytes > 0 && lastErrMsg == "" {
		latest.Status = models.RecordingStatusCompleted
		latest.Error = ""
	} else {
		if lastErrMsg == "" {
			lastErrMsg = "recording ended without writing media data"
		}
		latest.Status = models.RecordingStatusFailed
		latest.Error = truncateRecordingError(lastErrMsg)
	}
	log.Printf("[recordings] recording finished id=%s status=%s attempts=%d bytes=%d error=%q",
		latest.ID, latest.Status, attempt, latest.OutputSizeBytes, latest.Error)
	if err := s.repo.Update(context.Background(), latest); err != nil {
		log.Printf("[recordings] finalize recording update failed: %v", err)
	} else if latest.Status == models.RecordingStatusCompleted {
		s.queueComskip(latest.ID)
	}
}

// Appending a new MPEG-TS muxer output after a reconnect resets its PTS/DTS.
// Remux the completed file so players see one continuous seekable timeline.
func remuxRecordingTimestamps(ffmpegPath, path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".recording-remux-*.ts")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)

	cmd := execCommandContext(context.Background(), ffmpegPath,
		"-nostdin", "-loglevel", "error", "-y",
		"-i", path, "-map", "0", "-c", "copy",
		"-mpegts_flags", "+resend_headers", "-f", "mpegts", tmpPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, truncateRecordingError(stderr.String()))
	}
	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("remux produced no media data")
	}
	return os.Rename(tmpPath, path)
}

// recordingAttemptResult describes how one ffmpeg capture attempt ended.
type recordingAttemptResult struct {
	// stoppedAtSchedule is true when the recorder itself stopped ffmpeg at the
	// scheduled wall-clock stop time, i.e. the attempt ran to completion.
	stoppedAtSchedule bool
	written           int64
	err               error
	errMsg            string
	// mapsMatchedNothing: FFmpeg found no stream for the -map arguments.
	mapsMatchedNothing bool
	// content: what the attempt recorded (FFmpeg's -progress), for recordings that follow it.
	content time.Duration
	// reachedContent: a backfilled attempt whose content reached the stop time.
	reachedContent bool
	jumped         bool // content timestamps jumped: content is what it ran, and it carries on live
}

// runRecordingAttempt captures the source until stopAt (wall clock) or until
// ffmpeg exits on its own. The stop time is enforced by the recorder rather
// than ffmpeg's -t, which counts stream timestamps: live sources with forward
// timestamp jumps made -t end captures minutes early.
//
// A backfilled attempt (backfill.go) starts behind live and ends when its content reaches stopAt
// (-t), or at the latest at deadline. With content set, FFmpeg reports what it recorded
// (-progress) into it.
func (s *Service) runRecordingAttempt(ctx context.Context, recording models.Recording, stopAt, deadline time.Time, attempt int, streams recordingStreamChoice, backfill recordingBackfill, content *recordedContent) recordingAttemptResult {
	startedAt := time.Now()
	flags := os.O_CREATE | os.O_WRONLY
	if attempt == 1 {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	outputFile, err := os.OpenFile(recording.OutputPath, flags, 0o644)
	if err != nil {
		msg := fmt.Sprintf("open output file: %v", err)
		log.Printf("[recordings] attempt failed id=%s attempt=%d error=%q", recording.ID, attempt, msg)
		return recordingAttemptResult{err: err, errMsg: msg}
	}
	defer outputFile.Close()
	var startSize int64
	if info, err := outputFile.Stat(); err == nil {
		startSize = info.Size()
	}

	args := []string{"-nostdin"}
	var progressRead, progressWrite *os.File
	if content != nil {
		if progressRead, progressWrite, err = os.Pipe(); err == nil {
			args = append(args, "-progress", "pipe:3", "-stats_period", "1")
		}
	}
	args = append(args, "-loglevel", "warning")
	// An adaptive HLS channel: the chosen variant's media playlist and its audio renditions as
	// inputs (variant.go), so a master that changed since cannot swap the variant. Input options
	// apply to the next input only: every input gets them.
	inputOptions := []string{
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto,udp,rtp,rtmp",
		"-rw_timeout", fmt.Sprintf("%d", recordingReadTimeout.Microseconds()),
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "3",
	}
	if backfill.active() {
		inputOptions = append(inputOptions, "-live_start_index", strconv.Itoa(backfill.liveStartIndex))
	}
	inputs := streams.inputs
	if len(inputs) == 0 {
		inputs = []string{recording.SourceURL}
	}
	for _, input := range inputs {
		args = append(args, inputOptions...)
		args = append(args, "-i", input)
	}
	args = append(args, streams.args()...)
	if backfill.active() {
		args = append(args, "-t", strconv.FormatFloat(backfill.contentLimit(stopAt).Seconds(), 'f', 3, 64))
	}
	args = append(args,
		"-c", "copy",
		"-mpegts_flags", "+resend_headers",
		"-f", "mpegts",
		"pipe:1",
	)
	attemptCtx, cancelAttempt := context.WithDeadline(ctx, deadline)
	defer cancelAttempt()
	cmd := execCommandContext(attemptCtx, s.ffmpegPath, args...)
	// Stop gracefully so ffmpeg flushes buffered packets, then kill if it hangs.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = recordingStopGrace
	cmd.Stdout = outputFile
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if progressWrite != nil {
		cmd.ExtraFiles = []*os.File{progressWrite}
	}

	log.Printf("[recordings] attempt start id=%s attempt=%d remaining=%s",
		recording.ID, attempt, time.Until(stopAt).Round(time.Second))
	if err := cmd.Start(); err != nil {
		if progressWrite != nil {
			progressWrite.Close()
			progressRead.Close()
		}
		msg := fmt.Sprintf("start ffmpeg: %v", err)
		log.Printf("[recordings] attempt failed id=%s attempt=%d error=%q", recording.ID, attempt, msg)
		return recordingAttemptResult{err: err, errMsg: msg}
	}
	followed := followAttempt(cmd, progressRead, progressWrite, content, backfill, stopAt)
	waitErr := cmd.Wait()
	got, reached := followed()
	jumped := false
	var limit time.Duration
	if backfill.active() {
		limit = backfill.contentLimit(stopAt) - recordingBackfillReachSlack
	}
	if reached && time.Until(stopAt) > 2*recordingBackfillClockSlack {
		// Content cannot run that far ahead of the clock: its timestamps jumped. Not done; the
		// next attempt carries on live.
		log.Printf("[recordings] attempt id=%s attempt=%d: content reached the stop time %s early (timestamps jumped)",
			recording.ID, attempt, time.Until(stopAt).Round(time.Second))
		reached = false
		ran := time.Since(startedAt)
		if content != nil {
			// It holds about as much as it ran, not what the jumped timestamps say.
			content.done.Add(int64((ran - got) / time.Microsecond))
		}
		got, jumped = ran, true
	}

	written := int64(0)
	if info, err := outputFile.Stat(); err == nil {
		written = info.Size() - startSize
	}
	elapsed := time.Since(startedAt).Round(time.Second)
	errMsg := truncateRecordingError(stderr.String())
	mapsMatchedNothing := strings.Contains(stderr.String(), "matches no streams")

	if ctx.Err() == nil && written > 0 && (reached || (backfill.active() && got == 0 && waitErr == nil && !time.Now().Before(stopAt))) {
		// A backfilled attempt whose content reached the stop time (-t): it ran to completion,
		// however late by the wall clock. (Without progress reports: ending by itself after the
		// wall clock reached the stop time.)
		log.Printf("[recordings] attempt reached stop time in its content id=%s attempt=%d elapsed=%s bytes=%d content=%s",
			recording.ID, attempt, elapsed, written, got.Round(time.Second))
		return recordingAttemptResult{stoppedAtSchedule: true, written: written, content: got, reachedContent: true}
	}
	if ctx.Err() == nil && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
		if backfill.active() && got < limit {
			// A late attempt out of time before its content reached the stop time: the end is
			// missing, and the recording says so.
			msg := fmt.Sprintf("recording ran out of time %s before its content reached the stop time", (limit - got).Round(time.Second))
			log.Printf("[recordings] attempt timed out id=%s attempt=%d elapsed=%s bytes=%d content=%s: %s",
				recording.ID, attempt, elapsed, written, got.Round(time.Second), msg)
			return recordingAttemptResult{stoppedAtSchedule: true, written: written, content: got, errMsg: msg}
		}
		log.Printf("[recordings] attempt reached stop time id=%s attempt=%d elapsed=%s bytes=%d",
			recording.ID, attempt, elapsed, written)
		return recordingAttemptResult{stoppedAtSchedule: true, written: written, content: got}
	}
	if waitErr != nil && errMsg == "" {
		errMsg = waitErr.Error()
	}
	if ctx.Err() != nil {
		// Cancelled, deleted or shutting down: FFmpeg was interrupted, and its
		// last warning (often a harmless one about the source) is not why.
		log.Printf("[recordings] attempt stopped id=%s attempt=%d elapsed=%s bytes=%d: recording cancelled or service stopping",
			recording.ID, attempt, elapsed, written)
		return recordingAttemptResult{err: waitErr, errMsg: errMsg, written: written, mapsMatchedNothing: mapsMatchedNothing, content: got, jumped: jumped}
	}
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	log.Printf("[recordings] attempt ended early id=%s attempt=%d exit=%d elapsed=%s bytes=%d remaining=%s stderr=%q",
		recording.ID, attempt, exitCode, elapsed, written, time.Until(stopAt).Round(time.Second), errMsg)
	return recordingAttemptResult{err: waitErr, errMsg: errMsg, written: written, mapsMatchedNothing: mapsMatchedNothing, content: got, jumped: jumped}
}

// redactSourceURL keeps only scheme and host: IPTV source URLs often carry
// credentials in the path or query.
func redactSourceURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "<unparsed>"
	}
	return u.Scheme + "://" + u.Host
}

func (s *Service) handleInterruptedRecording(id string, ts time.Time) {
	defer s.lockRecording(id)()
	latest, err := s.Get(id)
	if err != nil {
		log.Printf("[recordings] reload interrupted recording failed: %v", err)
		return
	}
	if latest.Status == models.RecordingStatusCancelled {
		keepContentEnd(latest, ts)
		s.finalizeCancelledRecording(latest)
		return
	}
	msg := s.repairRecordingOutput(latest, "recording interrupted before scheduled stop time")
	s.finalizeFailure(*latest, ts, msg)
}

// keepContentEnd moves a cancelled recording's end back to where its content ends (a recording
// that looked back in its source's window holds less than the clock says).
func keepContentEnd(recording *models.Recording, contentEnd time.Time) {
	if recording.ActualEndAt == nil || contentEnd.Before(*recording.ActualEndAt) {
		recording.ActualEndAt = &contentEnd
	}
}

// recordingBackfillResumeTries: how often a backfilled recording asks again for a window that did
// not load before it carries on live.
const recordingBackfillResumeTries = 5

// waitBeforeRetry waits recordingRetryDelay; false when the recording was stopped meanwhile.
func (s *Service) waitBeforeRetry(ctx context.Context) bool {
	timer := time.NewTimer(recordingRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// setContentStart records where a running recording's content begins (ActualStartAt): earlier
// than the recorder started when it looked back in the source's window.
func (s *Service) setContentStart(id string, at time.Time) {
	defer s.lockRecording(id)()
	latest, err := s.Get(id)
	if err != nil || latest.Status != models.RecordingStatusRunning {
		return
	}
	latest.ActualStartAt = &at
	latest.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(context.Background(), latest); err != nil {
		log.Printf("[recordings] update content start failed: %v", err)
	}
}

func (s *Service) finalizeCancelledRecording(recording *models.Recording) {
	if recording == nil {
		return
	}
	recording.Error = s.repairRecordingOutput(recording, recording.Error)
	recording.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(context.Background(), recording); err != nil {
		log.Printf("[recordings] update cancelled recording %s failed: %v", recording.ID, err)
	}
}

func (s *Service) finalizeFailure(recording models.Recording, ts time.Time, msg string) {
	recording.Status = models.RecordingStatusFailed
	recording.Error = truncateRecordingError(msg)
	recording.ActualEndAt = &ts
	recording.UpdatedAt = ts
	if err := s.repo.Update(context.Background(), &recording); err != nil {
		log.Printf("[recordings] finalize failure update failed: %v", err)
	}
}

func truncateRecordingError(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}

var invalidFilenameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

func (s *Service) buildOutputPath(recording models.Recording) string {
	datePart := recording.StartAt.In(time.Local).Format("2006-01-02 1504")
	titlePart := sanitizeFilename(recording.Title)
	channelPart := truncateBytes(sanitizeFilename(recording.ChannelName), 60)
	if titlePart == "" {
		titlePart = channelPart
	}
	// A short ID suffix keeps two recordings of the same channel/minute/title from sharing
	// (and truncating) one file.
	suffix := recording.ID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	if suffix != "" {
		suffix = " [" + suffix + "]"
	}
	var head, tail string
	if recording.Type == models.RecordingTypeTimeBlock {
		head = fmt.Sprintf("%s - %s-%s", channelPart, datePart, recording.EndAt.In(time.Local).Format("1504"))
	} else {
		head = fmt.Sprintf("%s - %s", channelPart, datePart)
	}
	if titlePart != "" && (recording.Type != models.RecordingTypeTimeBlock || titlePart != channelPart) {
		budget := maxOutputFilenameBytes - len(head) - len(" - ") - len(suffix) - len(".ts")
		if budget > 0 {
			tail = " - " + truncateBytes(titlePart, budget)
		}
	}
	filename := head + tail + suffix + ".ts"
	return filepath.Join(s.outputDir, recording.UserID, filename)
}

// truncateBytes shortens value to at most n bytes without splitting a UTF-8 character.
func truncateBytes(value string, n int) string {
	if len(value) <= n {
		return value
	}
	for n > 0 && !utf8.RuneStart(value[n]) {
		n--
	}
	return strings.TrimRight(value[:n], ". ")
}

func sanitizeFilename(value string) string {
	value = invalidFilenameChars.ReplaceAllString(strings.TrimSpace(value), " ")
	value = strings.Join(strings.Fields(value), " ")
	value = strings.Trim(value, ". ")
	if value == "" {
		return "Recording"
	}
	return value
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// afterEmptyVariant is the choice after an attempt in which FFmpeg found nothing in the chosen
// variant. Before anything is recorded: everything, as before. Once something is recorded the file
// has the variant's layout and attempts append to it, so the previous maps are kept (looked up
// again on the next attempt): falling back to -map 0 would append a different stream layout.
func afterEmptyVariant(streams recordingStreamChoice, mapsMatchedNothing bool, recorded int64) recordingStreamChoice {
	if mapsMatchedNothing && streams.variant != "" && recorded == 0 {
		return recordingStreamChoice{maps: recordingAllStreams, final: true}
	}
	return streams
}
