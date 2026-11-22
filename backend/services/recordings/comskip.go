package recordings

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

// Commercial detection: once a recording has finished, comskip
// (https://github.com/erikkaashoek/Comskip) looks for its commercial breaks,
// which are kept as an EDL file next to the recording (the sidecar Kodi,
// Plex and Jellyfin's EDL tooling read). Off by default.
//
//   - One recording at a time, at low priority, in the background. Finished
//     recordings without an EDL are queued at startup and every
//     comskipRescan, so switching it on analyses the existing recordings too.
//   - comskip writes into a temporary directory; the EDL is moved next to the
//     recording only when the recording is still there, so a crash, a
//     shutdown or a deletion during the analysis leaves no partial or orphaned
//     file. An empty EDL marks a recording without commercials (comskip exits
//     1) as analysed.
//   - Switching it off stops a running analysis, and players are no longer
//     offered the breaks (Commercials); the EDL files stay, so switching it
//     on again needs no new analysis.
//   - A missing comskip binary is reported once and the recordings wait for
//     it; a failed analysis is retried at the next rescans, up to
//     comskipMaxAttempts times per server start.

// ComskipConfig is what the service needs of the comskip settings.
type ComskipConfig struct {
	Enabled bool
	Path    string // comskip binary, "comskip" on PATH when empty
	IniPath string // comskip.ini, defaultComskipIni when empty
}

const (
	comskipTimeout     = 3 * time.Hour
	comskipQueueSize   = 512
	comskipStartDelay  = time.Minute
	comskipRescan      = 10 * time.Minute
	comskipMaxAttempts = 3
	comskipNice        = "15"
	// comskipSettingsTTL is how long the settings are cached: Commercials
	// is asked for every recording a Jellyfin client opens.
	comskipSettingsTTL = 5 * time.Second
	// comskipWatch is how often a running analysis checks that comskip is
	// still switched on.
	comskipWatch = 10 * time.Second
)

// defaultComskipIni makes comskip write only the EDL file.
const defaultComskipIni = "output_edl=1\noutput_default=0\nverbose=0\n"

// errComskipStopped is a run that was stopped (switched off, recording
// deleted, shutdown): not a failure of the analysis.
var errComskipStopped = errors.New("comskip analysis stopped")

// SetComskip enables commercial detection with the settings cfg returns
// (changes apply without a restart).
func (s *Service) SetComskip(cfg func() ComskipConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comskipConfig = cfg
	s.comskipCachedAt = time.Time{}
	if s.comskipQueue == nil {
		s.comskipQueue = make(chan string, comskipQueueSize)
	}
}

// EDLPath is where a recording's commercial breaks are kept.
func EDLPath(outputPath string) string {
	return strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".edl"
}

// Commercials returns the commercial breaks of a recording file for players:
// none while commercial detection is off.
func (s *Service) Commercials(outputPath string) []models.CommercialBreak {
	if s == nil || !s.comskipSettings().Enabled {
		return nil
	}
	return ReadCommercials(outputPath)
}

// ReadCommercials returns the commercial breaks of a recording file (none
// without an EDL), as players should offer them: comskip often splits one
// break at the channel's bumpers and promos, so breaks less than
// commercialMergeGap apart are one break, and blips shorter than
// commercialMinLength are dropped.
func ReadCommercials(outputPath string) []models.CommercialBreak {
	if strings.TrimSpace(outputPath) == "" {
		return nil
	}
	data, err := os.ReadFile(EDLPath(outputPath))
	if err != nil {
		return nil
	}
	return mergeBreaks(parseEDL(data))
}

const (
	commercialMergeGap  = 30.0 // seconds
	commercialMinLength = 5.0  // seconds
)

func mergeBreaks(breaks []models.CommercialBreak) []models.CommercialBreak {
	sort.Slice(breaks, func(i, j int) bool { return breaks[i].Start < breaks[j].Start })
	var merged []models.CommercialBreak
	for _, b := range breaks {
		if n := len(merged); n > 0 && b.Start-merged[n-1].End < commercialMergeGap {
			if b.End > merged[n-1].End { // the package's own max takes ints
				merged[n-1].End = b.End
			}
			continue
		}
		merged = append(merged, b)
	}
	out := merged[:0]
	for _, b := range merged {
		if b.End-b.Start >= commercialMinLength {
			out = append(out, b)
		}
	}
	return out
}

// parseEDL reads "start end action" lines (seconds). Every action comskip
// writes (0 cut, 3 commercial break) marks a commercial.
func parseEDL(data []byte) []models.CommercialBreak {
	var breaks []models.CommercialBreak
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		start, err1 := strconv.ParseFloat(fields[0], 64)
		end, err2 := strconv.ParseFloat(fields[1], 64)
		// ParseFloat takes "NaN" and "Inf": an EDL is a sidecar anyone can
		// write, and such a break would break the segment responses.
		if err1 != nil || err2 != nil || math.IsNaN(start) || math.IsNaN(end) || math.IsInf(start, 0) || math.IsInf(end, 0) || end <= start || start < 0 {
			continue
		}
		breaks = append(breaks, models.CommercialBreak{Start: start, End: end})
	}
	return breaks
}

// queueComskip schedules a finished recording for analysis, once.
func (s *Service) queueComskip(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.comskipQueue == nil || s.comskipQueued[id] {
		return
	}
	select {
	case s.comskipQueue <- id:
		if s.comskipQueued == nil {
			s.comskipQueued = map[string]bool{}
		}
		s.comskipQueued[id] = true
	default:
		// The next rescan finds it again.
	}
}

// comskipLoop analyses queued recordings one at a time.
func (s *Service) comskipLoop(ctx context.Context) {
	defer s.wg.Done()
	select {
	case <-ctx.Done():
		return
	case <-time.After(comskipStartDelay):
	}
	s.queueMissingComskip(ctx)
	rescan := time.NewTicker(comskipRescan)
	defer rescan.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rescan.C:
			s.queueMissingComskip(ctx)
		case id := <-s.comskipQueue:
			s.runComskip(ctx, id)
			// Marked until the run is over, so a rescan during a long
			// analysis does not queue the same recording again.
			s.mu.Lock()
			delete(s.comskipQueued, id)
			s.mu.Unlock()
		}
	}
}

// queueMissingComskip queues the finished recordings without an EDL.
func (s *Service) queueMissingComskip(ctx context.Context) {
	cfg := s.comskipSettings()
	if !cfg.Enabled {
		return
	}
	if _, err := s.comskipBinary(cfg); err != nil {
		return // reported by comskipBinary; nothing can be analysed yet
	}
	recs, err := s.repo.List(ctx, models.RecordingListFilter{IncludeAll: true, Statuses: []models.RecordingStatus{models.RecordingStatusCompleted}})
	if err != nil {
		log.Printf("[comskip] list finished recordings failed: %v", err)
		return
	}
	for _, rec := range recs {
		if rec.Status != models.RecordingStatusCompleted || rec.OutputPath == "" || s.comskipGaveUp(rec.ID) {
			continue
		}
		if _, err := os.Stat(EDLPath(rec.OutputPath)); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := os.Stat(rec.OutputPath); err != nil {
			continue
		}
		s.queueComskip(rec.ID)
	}
}

func (s *Service) comskipGaveUp(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comskipFailures[id] >= comskipMaxAttempts
}

// comskipSettings returns the current settings, cached for
// comskipSettingsTTL.
func (s *Service) comskipSettings() ComskipConfig {
	s.mu.Lock()
	load, cached, at := s.comskipConfig, s.comskipCached, s.comskipCachedAt
	s.mu.Unlock()
	if load == nil {
		return ComskipConfig{}
	}
	if !at.IsZero() && time.Since(at) < comskipSettingsTTL {
		return cached
	}
	// Read without the service lock: loading the settings reads a file.
	cfg := load()
	s.mu.Lock()
	s.comskipCached, s.comskipCachedAt = cfg, time.Now()
	s.mu.Unlock()
	return cfg
}

// comskipBinary resolves the comskip binary, reporting a missing one once
// (until it is found again or the setting changes).
func (s *Service) comskipBinary(cfg ComskipConfig) (string, error) {
	binary := strings.TrimSpace(cfg.Path)
	if binary == "" {
		binary = "comskip"
	}
	path, err := exec.LookPath(binary)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if s.comskipMissing != binary {
			s.comskipMissing = binary
			log.Printf("[comskip] commercial detection is on, but comskip %q was not found (%v); recordings are analysed once it is", binary, err)
		}
		return "", err
	}
	s.comskipMissing = ""
	return path, nil
}

// cancelComskip stops the analysis of a recording, if one runs.
func (s *Service) cancelComskip(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.comskipRunning == id && s.comskipStop != nil {
		s.comskipStop()
	}
}

// runComskip analyses one recording.
func (s *Service) runComskip(ctx context.Context, id string) {
	cfg := s.comskipSettings()
	if !cfg.Enabled {
		return
	}
	// Registered before the recording is looked up, so a Delete from here
	// on stops the run (cancelComskip) instead of missing it.
	runCtx, stop := context.WithTimeout(ctx, comskipTimeout)
	defer stop()
	s.mu.Lock()
	s.comskipRunning, s.comskipStop = id, stop
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.comskipRunning, s.comskipStop = "", nil
		s.mu.Unlock()
	}()
	rec, err := s.repo.Get(runCtx, id)
	if err != nil || rec == nil || rec.Status != models.RecordingStatusCompleted || rec.OutputPath == "" {
		return
	}
	if _, err := os.Stat(EDLPath(rec.OutputPath)); err == nil {
		return
	}
	if _, err := os.Stat(rec.OutputPath); err != nil {
		return
	}
	binary, err := s.comskipBinary(cfg)
	if err != nil || runCtx.Err() != nil {
		return
	}
	// Switched off during the analysis: stop it.
	go func() {
		ticker := time.NewTicker(comskipWatch)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if !s.comskipSettings().Enabled {
					stop()
					return
				}
			}
		}
	}()

	started := time.Now()
	edl, output, err := analyseCommercials(runCtx, binary, cfg.IniPath, rec.OutputPath)
	took := time.Since(started).Round(time.Second)
	if runCtx.Err() != nil && !errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		err = errComskipStopped
	}
	switch {
	case errors.Is(err, errComskipStopped):
		log.Printf("[comskip] analysis of recording id=%s title=%q stopped after %s", rec.ID, rec.Title, took)
		return
	case err != nil:
		s.mu.Lock()
		if s.comskipFailures == nil {
			s.comskipFailures = map[string]int{}
		}
		s.comskipFailures[id]++
		attempt := s.comskipFailures[id]
		s.mu.Unlock()
		retry := "retried later"
		if attempt >= comskipMaxAttempts {
			retry = "not retried until restart"
		}
		log.Printf("[comskip] recording id=%s title=%q failed after %s (attempt %d, %s): %v %s",
			rec.ID, rec.Title, took, attempt, retry, err, truncateRecordingError(lastLines(output, 3)))
		return
	}
	if !s.keepEDL(rec.ID, rec.OutputPath, edl) {
		return
	}
	s.mu.Lock()
	delete(s.comskipFailures, id)
	s.mu.Unlock()
	if breaks := mergeBreaks(parseEDL(edl)); len(breaks) > 0 {
		log.Printf("[comskip] recording id=%s title=%q: %d commercial breaks in %s", rec.ID, rec.Title, len(breaks), took)
	} else {
		log.Printf("[comskip] recording id=%s title=%q: no commercials found in %s", rec.ID, rec.Title, took)
	}
}

// keepEDL writes a recording's EDL next to it, if the recording is still
// there (it may have been deleted during the analysis). Locked against
// Delete, so a deletion either sees the EDL and removes it or comes first.
func (s *Service) keepEDL(id, outputPath string, edl []byte) bool {
	defer s.lockRecording(id)()
	rec, err := s.repo.Get(context.Background(), id)
	if err != nil || rec == nil || rec.OutputPath != outputPath || rec.Status != models.RecordingStatusCompleted {
		return false
	}
	if _, err := os.Stat(outputPath); err != nil {
		return false
	}
	if err := writeFileAtomic(EDLPath(outputPath), edl); err != nil {
		log.Printf("[comskip] write edl failed id=%s: %v", id, err)
		return false
	}
	return true
}

// analyseCommercials runs comskip on a recording in a temporary directory
// and returns the EDL it wrote (empty: no commercials) and its output.
func analyseCommercials(ctx context.Context, binary, iniPath, input string) ([]byte, string, error) {
	// comskip runs in the temporary directory; recordings keep paths
	// relative to the server's directory.
	input, err := filepath.Abs(input)
	if err != nil {
		return nil, "", err
	}
	if iniPath = strings.TrimSpace(iniPath); iniPath != "" {
		if iniPath, err = filepath.Abs(iniPath); err != nil {
			return nil, "", err
		}
	}
	work, err := os.MkdirTemp("", "mediastorm-comskip-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(work)
	ini := iniPath
	if ini == "" {
		ini = filepath.Join(work, "comskip.ini")
		if err := os.WriteFile(ini, []byte(defaultComskipIni), 0o600); err != nil {
			return nil, "", err
		}
	}
	args := []string{"--ini=" + ini, "--output=" + work, input}
	name, cmdArgs := binary, args
	if nice, err := exec.LookPath("nice"); err == nil {
		name, cmdArgs = nice, append([]string{"-n", comskipNice, binary}, args...)
	}
	cmd := execCommandContext(ctx, name, cmdArgs...)
	cmd.Dir = work
	cmd.WaitDelay = 5 * time.Second
	out := &tailBuffer{max: comskipOutputTail}
	cmd.Stdout, cmd.Stderr = out, out
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, out.String(), ctx.Err()
	}
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	if edl, err := os.ReadFile(filepath.Join(work, base+".edl")); err == nil {
		return edl, out.String(), nil
	}
	// comskip exits 1 when it finds no commercials, but also when it
	// decoded no frame at all; only the former prints its verdict.
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 && strings.Contains(out.String(), comskipNoCommercials) {
		return []byte{}, out.String(), nil
	}
	if runErr == nil {
		runErr = errors.New("comskip wrote no EDL")
	}
	return nil, out.String(), runErr
}

const (
	// comskipNoCommercials is what comskip prints when it analysed the
	// recording and found no commercials.
	comskipNoCommercials = "Commercials were not found"
	// comskipOutputTail bounds the comskip output kept for the log (a
	// custom ini may make it verbose for hours).
	comskipOutputTail = 64 << 10
)

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// writeFileAtomic replaces path with data, never leaving a partial file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// removeEDL deletes a recording's EDL along with the recording.
func removeEDL(outputPath string) error {
	if strings.TrimSpace(outputPath) == "" {
		return nil
	}
	if err := os.Remove(EDLPath(outputPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete commercial breaks: %w", err)
	}
	return nil
}
