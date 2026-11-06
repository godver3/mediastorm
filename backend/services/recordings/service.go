package recordings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

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
	ErrCannotDeleteActive  = errors.New("cannot delete active recording")
)

const pollInterval = 15 * time.Second

var (
	recordingRetryDelay = 2 * time.Second
	execCommandContext  = exec.CommandContext
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
	recording, err := s.Get(id)
	if err != nil {
		return err
	}
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
	recording, err := s.Get(id)
	if err != nil {
		return err
	}
	if recording.Status == models.RecordingStatusPending && strings.TrimSpace(recording.RuleID) != "" {
		// Keep the schedule key as a tombstone so the rule expander does not
		// recreate a single occurrence the user removed from the queue.
		return s.Cancel(recording.ID)
	}
	if recording.Status == models.RecordingStatusRunning || recording.Status == models.RecordingStatusStarting {
		return ErrCannotDeleteActive
	}
	if strings.TrimSpace(recording.OutputPath) != "" {
		if err := os.Remove(recording.OutputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete recording file: %w", err)
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
						SourceURL:            channel.SourceURL,
						StartAt:              program.Start.UTC(),
						EndAt:                program.Stop.UTC(),
						PaddingBeforeSeconds: rule.PaddingBeforeSeconds,
						PaddingAfterSeconds:  rule.PaddingAfterSeconds,
						CreatedAt:            now,
						UpdatedAt:            now,
					}
					if _, err := s.rules.CreateScheduledRecordingIfAbsent(ctx, &job); err != nil {
						log.Printf("[recordings] materialize rule=%s channel=%s failed: %v", rule.ID, channel.ID, err)
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
	if !stopAt.After(now) {
		recording.Status = models.RecordingStatusFailed
		recording.Error = "recording window expired before start"
		recording.ActualEndAt = &now
		recording.UpdatedAt = now
		if err := s.repo.Update(context.Background(), &recording); err != nil {
			log.Printf("[recordings] mark expired recording failed: %v", err)
		}
		return
	}
	recording.Status = models.RecordingStatusStarting
	recording.OutputPath = s.buildOutputPath(recording)
	recording.UpdatedAt = now
	if err := s.repo.Update(context.Background(), &recording); err != nil {
		log.Printf("[recordings] update starting status failed: %v", err)
		return
	}
	if strings.TrimSpace(s.ffmpegPath) == "" {
		s.finalizeFailure(recording, now, "ffmpeg is not configured")
		return
	}
	if err := os.MkdirAll(filepath.Dir(recording.OutputPath), 0o755); err != nil {
		s.finalizeFailure(recording, now, fmt.Sprintf("create output directory: %v", err))
		return
	}

	ctx, cancel := context.WithCancel(s.ctx)
	recording.Status = models.RecordingStatusRunning
	recording.ActualStartAt = &now
	recording.UpdatedAt = now
	if err := s.repo.Update(context.Background(), &recording); err != nil {
		cancel()
		log.Printf("[recordings] update running status failed: %v", err)
		return
	}

	s.mu.Lock()
	s.active[recording.ID] = cancel
	s.mu.Unlock()
	liveusage.GetTracker().StartRecording(recording.ID, recording.UserID)
	defer func() {
		s.mu.Lock()
		delete(s.active, recording.ID)
		s.mu.Unlock()
		liveusage.GetTracker().EndRecording(recording.ID)
		cancel()
	}()

	attempt := 0
	lastErrMsg := ""
	for {
		remaining := time.Until(stopAt)
		if remaining <= 0 {
			break
		}
		attempt++

		waitErr, errMsg := s.runRecordingAttempt(ctx, recording, remaining, attempt == 1)
		finishedAt := time.Now().UTC()
		if ctx.Err() != nil {
			s.handleInterruptedRecording(recording.ID, finishedAt)
			return
		}
		if errMsg != "" {
			lastErrMsg = errMsg
		}

		remaining = time.Until(stopAt)
		if remaining <= 0 {
			if waitErr == nil {
				lastErrMsg = ""
			}
			break
		}

		if waitErr == nil {
			lastErrMsg = "source stream ended before scheduled stop time"
		}

		timer := time.NewTimer(recordingRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.handleInterruptedRecording(recording.ID, time.Now().UTC())
			return
		case <-timer.C:
		}
	}

	latest, err := s.Get(recording.ID)
	if err != nil {
		log.Printf("[recordings] reload recording after ffmpeg exit failed: %v", err)
		return
	}
	finishedAt := time.Now().UTC()
	if latest.Status == models.RecordingStatusCancelled {
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
	if err := s.repo.Update(context.Background(), latest); err != nil {
		log.Printf("[recordings] finalize recording update failed: %v", err)
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

func (s *Service) runRecordingAttempt(ctx context.Context, recording models.Recording, remaining time.Duration, truncate bool) (error, string) {
	durationSec := int(remaining.Seconds())
	if durationSec < 1 {
		durationSec = 1
	}

	flags := os.O_CREATE | os.O_WRONLY
	if truncate {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	outputFile, err := os.OpenFile(recording.OutputPath, flags, 0o644)
	if err != nil {
		return err, fmt.Sprintf("open output file: %v", err)
	}
	defer outputFile.Close()

	args := []string{
		"-nostdin",
		"-loglevel", "warning",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto,udp,rtp,rtmp",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "3",
		"-i", recording.SourceURL,
		"-map", "0",
		"-c", "copy",
		"-t", fmt.Sprintf("%d", durationSec),
		"-mpegts_flags", "+resend_headers",
		"-f", "mpegts",
		"pipe:1",
	}
	cmd := execCommandContext(ctx, s.ffmpegPath, args...)
	cmd.Stdout = outputFile
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err, fmt.Sprintf("start ffmpeg: %v", err)
	}
	waitErr := cmd.Wait()
	errMsg := strings.TrimSpace(stderr.String())
	if waitErr != nil && errMsg == "" {
		errMsg = waitErr.Error()
	}
	return waitErr, truncateRecordingError(errMsg)
}

func (s *Service) handleInterruptedRecording(id string, ts time.Time) {
	latest, err := s.Get(id)
	if err != nil {
		log.Printf("[recordings] reload interrupted recording failed: %v", err)
		return
	}
	if latest.Status == models.RecordingStatusCancelled {
		s.finalizeCancelledRecording(latest)
		return
	}
	msg := s.repairRecordingOutput(latest, "recording interrupted before scheduled stop time")
	s.finalizeFailure(*latest, ts, msg)
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
	channelPart := sanitizeFilename(recording.ChannelName)
	if titlePart == "" {
		titlePart = channelPart
	}
	filename := fmt.Sprintf("%s - %s - %s.ts", channelPart, datePart, titlePart)
	if recording.Type == models.RecordingTypeTimeBlock {
		endPart := recording.EndAt.In(time.Local).Format("1504")
		if titlePart != "" && titlePart != channelPart {
			filename = fmt.Sprintf("%s - %s-%s - %s.ts", channelPart, datePart, endPart, titlePart)
		} else {
			filename = fmt.Sprintf("%s - %s-%s.ts", channelPart, datePart, endPart)
		}
	}
	return filepath.Join(s.outputDir, recording.UserID, filename)
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
