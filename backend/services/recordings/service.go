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
	repo       datastore.RecordingRepository
	ffmpegPath string
	outputDir  string

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	running bool
	wg      sync.WaitGroup
	active  map[string]context.CancelFunc
}

func NewService(repo datastore.RecordingRepository, ffmpegPath, outputDir string) *Service {
	if strings.TrimSpace(outputDir) == "" {
		outputDir = filepath.Join("cache", "recordings")
	}
	return &Service{
		repo:       repo,
		ffmpegPath: strings.TrimSpace(ffmpegPath),
		outputDir:  outputDir,
		active:     make(map[string]context.CancelFunc),
	}
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
		attempt++
		remaining := time.Until(stopAt)
		if remaining <= 0 {
			break
		}

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
		if err := os.Remove(latest.OutputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[recordings] cleanup cancelled file failed: %v", err)
		}
		return
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
		if err := os.Remove(latest.OutputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[recordings] cleanup cancelled file failed: %v", err)
		}
		return
	}
	s.finalizeFailure(*latest, ts, "recording interrupted before scheduled stop time")
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
