package epg

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/apiusage"
	"novastream/internal/netproxy"
	"novastream/models"
)

const (
	defaultHTTPTimeout = 120 * time.Second // XMLTV files can be large
	maxEPGFileSize     = 100 * 1024 * 1024 // 100 MB max
	epgCacheDir        = "cache/epg"
	epgCacheFile       = "epg.json"
)

// Service handles EPG data fetching, parsing, and querying.
type Service struct {
	cfgManager *config.Manager
	storageDir string
	client     *http.Client

	mu                      sync.RWMutex
	schedule                *models.EPGSchedule
	refreshing              bool
	lastExhaustedRefresh    time.Time // last automatic refresh started by RefreshIfExhausted
	lastError               string
	discoveredSourceCount   int
	restoreDone             chan struct{}
	hdHomeRunProfileSources func(config.Settings) []config.LivePlaylistSource
	hdHomeRunMu             sync.Mutex
	hdHomeRunCache          map[string]hdHomeRunGuideCache
	hdHomeRunGuideURL       string         // test endpoint; production always uses SiliconDust
	xmltvInvalidTimes       map[string]int // optional diagnostics on the isolated HDHomeRun importer
	xmltvUserAgent          string         // optional application identity for native guide requests

	// normalizedIDIndex/normalizedNameIndex let findProgramsByChannelMatch resolve most
	// misses with an O(1) lookup instead of the linear scan it used to always fall back to
	// (normalizeChannelID(candidate) == normalizeChannelID(every program key), for every
	// program key, for every miss) - that scan, multiplied by a caller passing many channel
	// IDs at once (sports auto-link, live search), was slow enough to make those endpoints
	// hang outright on a large (tens of thousands of channels) playlist. Built once whenever
	// schedule is (re)assigned, not per lookup. Keys shared by different guide channels are
	// left out, and there is no partial-match fallback beyond these two indexes.
	normalizedIDIndex   map[string]string
	normalizedNameIndex map[string]string
	// exactNameIndex is consulted before the normalized indexes. Normalization strips
	// HD/SD suffixes, so a tuner's "WSIM" and "WSIM-SD" subchannels collide there and
	// are both dropped as ambiguous, while their exact display names stay distinct.
	exactNameIndex map[string]string
}

type epgXMLTVSource struct {
	name     string
	url      string
	proxyURL string
	priority int
	inferred bool
}

type epgXtreamSource struct {
	name     string
	settings config.Settings
}

// NewService creates a new EPG service.
func NewService(storageDir string, cfgManager *config.Manager) *Service {
	initialSchedule := &models.EPGSchedule{
		Channels: make(map[string]models.EPGChannel),
		Programs: make(map[string][]models.EPGProgram),
	}
	s := &Service{
		cfgManager: cfgManager,
		storageDir: storageDir,
		client: apiusage.TrackClient(&http.Client{
			Timeout: defaultHTTPTimeout,
		}, "Live TV", "EPG fetch"),
		schedule:    initialSchedule,
		restoreDone: make(chan struct{}),
	}

	// Ensure cache directory exists
	cacheDir := filepath.Join(storageDir, "epg")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[epg] failed to create cache directory: %v", err)
	}

	// A large guide cache can take close to a second to decode. Restore it in
	// the background so backend health does not depend on guide size. Only
	// install the cache if a concurrent refresh has not already replaced the
	// initial empty schedule.
	go func() {
		defer close(s.restoreDone)
		started := time.Now()
		schedule, err := s.readFromDisk()
		if err != nil {
			log.Printf("[epg] no cached EPG data found or error loading: %v", err)
			return
		}

		if !s.installRestoredSchedule(initialSchedule, schedule) {
			log.Printf("[epg] skipped stale cached EPG data after concurrent refresh")
			return
		}
		now := time.Now().UTC()
		_, upcoming, _, latest, hoursLeft := scheduleCoverage(schedule, now).logFields(now)
		log.Printf("[epg] loaded cached EPG data: %d channels, %d programs in %s lastUpdated=%s channelsUpcoming=%d latestStop=%s hoursLeft=%d",
			len(schedule.Channels), countSchedulePrograms(schedule), time.Since(started),
			schedule.LastUpdated.UTC().Format(time.RFC3339), upcoming, latest, hoursLeft)
	}()

	return s
}

func (s *Service) installRestoredSchedule(initial, restored *models.EPGSchedule) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.schedule != initial {
		return false
	}
	s.schedule = restored
	s.rebuildScheduleIndexLocked()
	return true
}

// rebuildScheduleIndexLocked recomputes normalizedIDIndex/normalizedNameIndex from the
// current s.schedule. Caller must hold s.mu (write lock) - called immediately after every
// s.schedule reassignment so the indexes never go stale relative to the data they cover.
func (s *Service) rebuildScheduleIndexLocked() {
	// Keys shared by different guide channels are dropped (same rule as GetNowPlaying)
	// so a playlist channel never binds to whichever entry map iteration saw last.
	idIndex := make(map[string]string, len(s.schedule.Programs))
	ambiguousIDs := make(map[string]struct{})
	for channelID := range s.schedule.Programs {
		addUnambiguousIndexKey(idIndex, ambiguousIDs, normalizeChannelID(channelID), channelID)
	}
	nameIndex := make(map[string]string, len(s.schedule.Channels))
	ambiguousNames := make(map[string]struct{})
	exactIndex := make(map[string]string, len(s.schedule.Channels))
	ambiguousExact := make(map[string]struct{})
	for channelID, ch := range s.schedule.Channels {
		if _, hasPrograms := s.schedule.Programs[channelID]; !hasPrograms {
			continue
		}
		for _, name := range append([]string{ch.Name}, ch.Aliases...) {
			addUnambiguousIndexKey(nameIndex, ambiguousNames, normalizeChannelID(name), channelID)
			addUnambiguousIndexKey(exactIndex, ambiguousExact, exactChannelName(name), channelID)
		}
	}
	s.normalizedIDIndex = idIndex
	s.normalizedNameIndex = nameIndex
	s.exactNameIndex = exactIndex
}

func exactChannelName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func addUnambiguousIndexKey(index map[string]string, ambiguous map[string]struct{}, key, channelID string) {
	if key == "" {
		return
	}
	if _, isAmbiguous := ambiguous[key]; isAmbiguous {
		return
	}
	if existing, exists := index[key]; exists && existing != channelID {
		delete(index, key)
		ambiguous[key] = struct{}{}
		return
	}
	index[key] = channelID
}

// countPrograms returns total number of programs across all channels.
func (s *Service) countPrograms() int {
	return countSchedulePrograms(s.schedule)
}

func countSchedulePrograms(schedule *models.EPGSchedule) int {
	if schedule == nil {
		return 0
	}
	count := 0
	for _, progs := range schedule.Programs {
		count += len(progs)
	}
	return count
}

func liveSourceEnabled(source config.LivePlaylistSource) bool {
	return source.Enabled == nil || *source.Enabled
}

func configuredLiveSources(settings config.Settings) []config.LivePlaylistSource {
	if len(settings.Live.Sources) > 0 {
		return settings.Live.Sources
	}
	return settings.Live.PlaylistSources
}

func isEPGRefreshEnabled(settings config.Settings) bool {
	if settings.Live.EPG.Enabled {
		return true
	}
	for _, source := range configuredLiveSources(settings) {
		if liveSourceEnabled(source) && source.EPG.Enabled {
			return true
		}
	}
	return false
}

func appendEPGXMLTVSources(result []epgXMLTVSource, epgSettings config.EPGSettings, namePrefix, proxyURL string, priorityOffset int) []epgXMLTVSource {
	if strings.TrimSpace(epgSettings.XmltvUrl) != "" {
		result = append(result, epgXMLTVSource{
			name:     namePrefix,
			url:      strings.TrimSpace(epgSettings.XmltvUrl),
			proxyURL: strings.TrimSpace(proxyURL),
			priority: priorityOffset,
		})
	}

	for _, source := range epgSettings.Sources {
		if !source.Enabled {
			continue
		}
		if strings.TrimSpace(strings.ToLower(source.Type)) != "xmltv" {
			log.Printf("[epg] skipping unknown source type: %s", source.Type)
			continue
		}
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		name := strings.TrimSpace(source.Name)
		if name == "" {
			name = fmt.Sprintf("%s source", namePrefix)
		} else if strings.TrimSpace(namePrefix) != "" {
			name = namePrefix + ": " + name
		}
		result = append(result, epgXMLTVSource{
			name:     name,
			url:      strings.TrimSpace(source.URL),
			proxyURL: strings.TrimSpace(proxyURL),
			priority: priorityOffset + source.Priority,
		})
	}

	return result
}

func collectXMLTVSources(settings config.Settings) []epgXMLTVSource {
	var result []epgXMLTVSource
	if settings.Live.EPG.Enabled {
		result = appendEPGXMLTVSources(result, settings.Live.EPG, "global EPG", settings.Live.ProxyURL, 0)
	}

	for i, source := range configuredLiveSources(settings) {
		if !liveSourceEnabled(source) || !source.EPG.Enabled {
			continue
		}
		name := strings.TrimSpace(source.Name)
		if name == "" {
			name = fmt.Sprintf("live source %d", i+1)
		}
		proxyURL := source.ProxyURL
		if strings.TrimSpace(proxyURL) == "" {
			proxyURL = settings.Live.ProxyURL
		}
		result = appendEPGXMLTVSources(result, source.EPG, name, proxyURL, 10000+i*1000)
	}

	for i, source := range discoveryPlaylists(settings) {
		if guide := inferPlaylistGuide(source.PlaylistURL); guide != "" {
			result = append(result, epgXMLTVSource{name: fmt.Sprintf("inferred Xtream guide %d", i+1), url: guide, proxyURL: source.ProxyURL, priority: 20000 + i, inferred: true})
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].priority < result[j].priority
	})
	return result
}

func countConfiguredXMLTVSources(settings config.Settings) int {
	return len(collectXMLTVSources(settings))
}

func configuredSourceModeCounts(settings config.Settings) (m3uCount, xtreamCount int) {
	for _, source := range configuredLiveSources(settings) {
		if !liveSourceEnabled(source) {
			continue
		}
		mode := strings.TrimSpace(strings.ToLower(source.Mode))
		if mode == "" {
			mode = "m3u"
		}
		switch mode {
		case "xtream":
			xtreamCount++
		default:
			m3uCount++
		}
	}
	return m3uCount, xtreamCount
}

func collectXtreamSources(settings config.Settings) []epgXtreamSource {
	var result []epgXtreamSource
	sources := configuredLiveSources(settings)
	if len(sources) > 0 {
		for i, source := range sources {
			if !liveSourceEnabled(source) || !strings.EqualFold(strings.TrimSpace(source.Mode), "xtream") {
				continue
			}
			if strings.TrimSpace(source.XtreamHost) == "" ||
				strings.TrimSpace(source.XtreamUsername) == "" ||
				strings.TrimSpace(source.XtreamPassword) == "" {
				continue
			}
			if !settings.Live.EPG.Enabled && !source.EPG.Enabled {
				continue
			}
			sourceSettings := settings
			sourceSettings.Live.Mode = "xtream"
			sourceSettings.Live.ProxyURL = strings.TrimSpace(source.ProxyURL)
			if sourceSettings.Live.ProxyURL == "" {
				sourceSettings.Live.ProxyURL = settings.Live.ProxyURL
			}
			sourceSettings.Live.XtreamHost = strings.TrimSpace(source.XtreamHost)
			sourceSettings.Live.XtreamUsername = strings.TrimSpace(source.XtreamUsername)
			sourceSettings.Live.XtreamPassword = strings.TrimSpace(source.XtreamPassword)
			name := strings.TrimSpace(source.Name)
			if name == "" {
				name = fmt.Sprintf("xtream source %d", i+1)
			}
			result = append(result, epgXtreamSource{name: name, settings: sourceSettings})
		}
		return result
	}

	if strings.EqualFold(settings.Live.Mode, "xtream") &&
		strings.TrimSpace(settings.Live.XtreamHost) != "" &&
		strings.TrimSpace(settings.Live.XtreamUsername) != "" &&
		strings.TrimSpace(settings.Live.XtreamPassword) != "" &&
		settings.Live.EPG.Enabled {
		result = append(result, epgXtreamSource{name: "global Xtream", settings: settings})
	}
	return result
}

// GetStatus returns the current EPG service status.
func (s *Service) GetStatus() models.EPGStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	settings, err := s.cfgManager.Load()
	if err != nil {
		return models.EPGStatus{Enabled: false}
	}

	status := models.EPGStatus{
		Enabled:      isEPGRefreshEnabled(settings) || len(s.hdHomeRunSources(settings)) > 0,
		ChannelCount: len(s.schedule.Channels),
		ProgramCount: s.countPrograms(),
		Refreshing:   s.refreshing,
		LastError:    s.lastError,
		SourceCount:  countConfiguredXMLTVSources(settings) + len(s.hdHomeRunSources(settings)) + s.discoveredSourceCount,
	}

	if !s.schedule.LastUpdated.IsZero() {
		status.LastRefresh = &s.schedule.LastUpdated
	}

	return status
}

// Refresh fetches and parses EPG data from all configured sources.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	if s.refreshing {
		s.mu.Unlock()
		log.Println("[epg] refresh already in progress, skipping duplicate request")
		return nil // Not an error - refresh is happening, which is what we want
	}
	s.refreshing = true
	s.lastError = ""
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.refreshing = false
		s.mu.Unlock()
	}()

	settings, err := s.cfgManager.Load()
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("failed to load settings: %w", err)
	}

	sourceEPGConfigured := 0
	sourceEPGEnabled := 0
	for _, source := range configuredLiveSources(settings) {
		if strings.TrimSpace(source.EPG.XmltvUrl) != "" || len(source.EPG.Sources) > 0 {
			sourceEPGConfigured++
		}
		if liveSourceEnabled(source) && source.EPG.Enabled {
			sourceEPGEnabled++
		}
	}
	xtreamSources := collectXtreamSources(settings)
	m3uSourceCount, xtreamSourceCount := configuredSourceModeCounts(settings)
	log.Printf("[epg] refresh config: globalEnabled=%v configuredSources=%d m3uSources=%d xtreamSourcesConfigured=%d globalXMLTVConfigured=%v globalEPGSources=%d sourceEPGConfigured=%d sourceEPGEnabled=%d xtreamEPGFetchSources=%d",
		settings.Live.EPG.Enabled,
		len(configuredLiveSources(settings)),
		m3uSourceCount,
		xtreamSourceCount,
		strings.TrimSpace(settings.Live.EPG.XmltvUrl) != "",
		len(settings.Live.EPG.Sources),
		sourceEPGConfigured,
		sourceEPGEnabled,
		len(xtreamSources),
	)

	if !isEPGRefreshEnabled(settings) && len(s.hdHomeRunSources(settings)) == 0 {
		return errors.New("EPG is disabled")
	}

	// Create new schedule
	newSchedule := &models.EPGSchedule{
		Channels:    make(map[string]models.EPGChannel),
		Programs:    make(map[string][]models.EPGProgram),
		LastUpdated: time.Now().UTC(),
	}
	var refreshErrors []string
	hdSources := s.hdHomeRunSources(settings)
	for _, source := range hdSources {
		if err := s.fetchHDHomeRunEPG(ctx, source, newSchedule); err != nil {
			refreshErrors = append(refreshErrors, fmt.Sprintf("HDHomeRun %s: %v", source.Name, err))
		}
	}

	hdChannelIDs := make(map[string]bool, len(newSchedule.Programs))
	for id := range newSchedule.Programs {
		hdChannelIDs[id] = true
	}

	for _, source := range xtreamSources {
		sourceSettings := source.settings
		log.Printf("[epg] fetching Xtream EPG source name=%q proxyConfigured=%v", source.name, strings.TrimSpace(sourceSettings.Live.ProxyURL) != "")
		if err := s.fetchXtreamEPG(ctx, &sourceSettings, newSchedule); err != nil {
			log.Printf("[epg] Xtream EPG fetch failed for source name=%q: %v", source.name, err)
			refreshErrors = append(refreshErrors, fmt.Sprintf("%s Xtream EPG: %v", source.name, err))
		} else {
			newSchedule.SourceType = "xtream"
		}
	}

	xmltvSources := collectXMLTVSources(settings)
	discovered := s.discoverPlaylistHeaders(ctx, settings)
	seenGuides := make(map[string]bool)
	for _, source := range xmltvSources {
		seenGuides[source.url] = true
	}
	discoveredCount := 0
	for _, source := range discovered {
		if !seenGuides[source.url] {
			xmltvSources = append(xmltvSources, source)
			seenGuides[source.url] = true
			discoveredCount++
		}
	}
	log.Printf("[epg] guide sources resolved configuredOrInferred=%d playlistHeaders=%d", len(xmltvSources)-discoveredCount, discoveredCount)
	s.mu.Lock()
	s.discoveredSourceCount = discoveredCount
	s.mu.Unlock()
	for _, source := range xmltvSources {
		log.Printf("[epg] fetching XMLTV source name=%q proxyConfigured=%v", source.name, strings.TrimSpace(source.proxyURL) != "")
		beforeChannels := len(newSchedule.Channels)
		beforePrograms := 0
		for _, programs := range newSchedule.Programs {
			beforePrograms += len(programs)
		}
		var userAgents []string
		if source.inferred {
			userAgents = xtreamUserAgents
		}
		if err := s.fetchXMLTVWithProxyAndUserAgents(ctx, source.url, source.proxyURL, newSchedule, userAgents); err != nil {
			if source.inferred {
				err = errors.New("discovered guide fetch failed")
			}
			log.Printf("[epg] failed to fetch XMLTV source name=%q: %v", source.name, err)
			refreshErrors = append(refreshErrors, fmt.Sprintf("%s: %v", source.name, err))
		} else if newSchedule.SourceType == "" {
			newSchedule.SourceType = "xmltv"
		}
		afterPrograms := 0
		for _, programs := range newSchedule.Programs {
			afterPrograms += len(programs)
		}
		log.Printf("[epg] XMLTV source parsed name=%q addedChannels=%d addedPrograms=%d totalChannels=%d totalPrograms=%d",
			source.name,
			len(newSchedule.Channels)-beforeChannels,
			afterPrograms-beforePrograms,
			len(newSchedule.Channels),
			afterPrograms,
		)
	}

	if newSchedule.SourceType == "" && len(xmltvSources) > 0 {
		newSchedule.SourceType = "xmltv"
	}

	for _, source := range xtreamSources {
		sourceSettings := source.settings
		if err := s.supplementWithXtreamPerChannel(ctx, &sourceSettings, newSchedule); err != nil {
			log.Printf("[epg] per-channel supplement failed for source name=%q (non-fatal): %v", source.name, err)
			refreshErrors = append(refreshErrors, fmt.Sprintf("%s per-channel EPG: %v", source.name, err))
		}
	}

	// Prune old programs based on retention
	retentionDays := settings.Live.EPG.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 7
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	futureDays := retentionDays
	if len(hdSources) > 0 && futureDays < 14 {
		futureDays = 14
	}
	futureLimit := time.Now().Add(time.Duration(retentionDays) * 24 * time.Hour)
	hdFutureLimit := time.Now().Add(time.Duration(futureDays) * 24 * time.Hour)

	// When a time offset is configured, programs are shifted at display time.
	// Extend both cutoffs by the absolute offset to avoid premature pruning.
	if offsetMin := settings.Live.EPG.TimeOffsetMinutes; offsetMin != 0 {
		abs := time.Duration(offsetMin) * time.Minute
		if abs < 0 {
			abs = -abs
		}
		cutoff = cutoff.Add(-abs)
		futureLimit = futureLimit.Add(abs)
		hdFutureLimit = hdFutureLimit.Add(abs)
	}

	for channelID, programs := range newSchedule.Programs {
		channelFutureLimit := futureLimit
		if hdChannelIDs[channelID] {
			channelFutureLimit = hdFutureLimit
		}
		var filtered []models.EPGProgram
		for _, prog := range programs {
			if prog.Stop.After(cutoff) && prog.Start.Before(channelFutureLimit) {
				filtered = append(filtered, prog)
			}
		}
		newSchedule.Programs[channelID] = filtered
		if hdChannelIDs[channelID] && len(filtered) != len(programs) {
			log.Printf("[hdhomerun-epg] retention channel=%q before=%d after=%d cutoff=%s futureLimit=%s", channelID, len(programs), len(filtered), cutoff.UTC().Format(time.RFC3339), channelFutureLimit.UTC().Format(time.RFC3339))
		}
	}

	programCount := countSchedulePrograms(newSchedule)
	if programCount == 0 {
		detail := "no guide programs were returned by the configured EPG sources"
		if len(refreshErrors) > 0 {
			detail = strings.Join(refreshErrors, "; ")
		}
		refreshErr := fmt.Errorf("EPG refresh produced no usable guide data: %s", detail)
		s.mu.Lock()
		s.lastError = refreshErr.Error()
		s.mu.Unlock()
		log.Printf("[epg] refresh failed; retaining previous schedule: %v", refreshErr)
		return refreshErr
	}

	// Only replace the schedule after the refresh produced usable guide data.
	s.mu.Lock()
	s.schedule = newSchedule
	s.rebuildScheduleIndexLocked()
	if len(refreshErrors) > 0 {
		s.lastError = strings.Join(refreshErrors, "; ")
	} else {
		s.lastError = ""
	}
	s.mu.Unlock()

	// Save to disk
	if err := s.saveToDisk(); err != nil {
		log.Printf("[epg] failed to save EPG to disk: %v", err)
	}

	now := time.Now().UTC()
	coverage := scheduleCoverage(newSchedule, now)
	withPrograms, upcoming, _, latest, hoursLeft := coverage.logFields(now)
	log.Printf("[epg] refresh complete: %d channels, %d programs channelsWithPrograms=%d channelsUpcoming=%d latestStop=%s hoursLeft=%d",
		len(newSchedule.Channels), programCount, withPrograms, upcoming, latest, hoursLeft)
	if coverage.exhausted() {
		log.Printf("[epg] refresh returned a guide that is already mostly over; the source likely publishes only the current event per channel")
	}

	return nil
}

// fetchXtreamEPG fetches EPG data from the Xtream Codes xmltv.php endpoint.
func (s *Service) fetchXtreamEPG(ctx context.Context, settings *config.Settings, schedule *models.EPGSchedule) error {
	host := strings.TrimRight(settings.Live.XtreamHost, "/")
	username := settings.Live.XtreamUsername
	password := settings.Live.XtreamPassword

	epgURL := fmt.Sprintf("%s/xmltv.php?username=%s&password=%s",
		host, url.QueryEscape(username), url.QueryEscape(password))

	return s.fetchXMLTVWithProxyAndUserAgents(ctx, epgURL, settings.Live.ProxyURL, schedule, xtreamUserAgents)
}

// fetchXMLTV fetches and parses XMLTV data from a URL.
func (s *Service) fetchXMLTV(ctx context.Context, xmltvURL string, schedule *models.EPGSchedule) error {
	proxyURL := ""
	if s.cfgManager != nil {
		if settings, err := s.cfgManager.Load(); err == nil {
			proxyURL = strings.TrimSpace(settings.Live.ProxyURL)
		}
	}
	return s.fetchXMLTVWithProxy(ctx, xmltvURL, proxyURL, schedule)
}

func (s *Service) fetchXMLTVWithProxy(ctx context.Context, xmltvURL, proxyURL string, schedule *models.EPGSchedule) error {
	return s.fetchXMLTVWithProxyAndUserAgents(ctx, xmltvURL, proxyURL, schedule, nil)
}

// Keep the existing error text while letting native guide providers decide
// whether an HTTP response warrants a bounded authentication retry.
type epgHTTPStatusError struct {
	StatusCode int
}

func (e *epgHTTPStatusError) Error() string {
	return fmt.Sprintf("EPG fetch returned status %d", e.StatusCode)
}

func (s *Service) fetchXMLTVWithProxyAndUserAgents(ctx context.Context, xmltvURL, proxyURL string, schedule *models.EPGSchedule, userAgents []string) error {
	started := time.Now()
	hostLabel := xmltvHostLabel(xmltvURL)
	proxyConfigured := strings.TrimSpace(proxyURL) != ""
	log.Printf("[epg] XMLTV HTTP request start host=%q proxyConfigured=%v", hostLabel, proxyConfigured)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xmltvURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// Add Accept-Encoding for gzip
	req.Header.Set("Accept-Encoding", "gzip")
	if s.xmltvUserAgent != "" {
		req.Header.Set("User-Agent", s.xmltvUserAgent)
	}

	client := s.httpClient(proxyURL)
	var resp *http.Response
	if len(userAgents) > 0 {
		resp, _, err = doXtreamRequestWithUserAgents(req, client, userAgents)
	} else {
		resp, err = client.Do(req)
	}
	if err != nil {
		log.Printf("[epg] XMLTV HTTP request failed host=%q elapsed=%s", hostLabel, time.Since(started).Round(time.Millisecond))
		return errors.New("EPG HTTP request failed (connection, timeout, or redirect error)")
	}
	defer resp.Body.Close()

	log.Printf("[epg] XMLTV HTTP response host=%q status=%d contentLength=%d encoding=%q elapsed=%s",
		hostLabel,
		resp.StatusCode,
		resp.ContentLength,
		resp.Header.Get("Content-Encoding"),
		time.Since(started).Round(time.Millisecond),
	)

	if resp.StatusCode != http.StatusOK {
		return &epgHTTPStatusError{StatusCode: resp.StatusCode}
	}

	// Handle gzip compression
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" || strings.HasSuffix(xmltvURL, ".gz") {
		log.Printf("[epg] XMLTV gzip decode start host=%q elapsed=%s", hostLabel, time.Since(started).Round(time.Millisecond))
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("decompress gzip: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	// Limit reader size
	limited := io.LimitReader(reader, maxEPGFileSize+1)

	if err := s.parseXMLTV(limited, schedule); err != nil {
		log.Printf("[epg] XMLTV parse failed host=%q elapsed=%s error=%v", hostLabel, time.Since(started).Round(time.Millisecond), err)
		return err
	}
	log.Printf("[epg] XMLTV parse finished host=%q elapsed=%s", hostLabel, time.Since(started).Round(time.Millisecond))
	return nil
}

func xmltvHostLabel(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "invalid-url"
	}
	return u.Host
}

func (s *Service) httpClient(proxyURL string) *http.Client {
	if strings.TrimSpace(proxyURL) == "" {
		return s.client
	}
	client, err := netproxy.NewHTTPClient(defaultHTTPTimeout, proxyURL)
	if err != nil {
		log.Printf("[epg] invalid proxy URL %q: %v", proxyURL, err)
		return s.client
	}
	return apiusage.TrackClient(client, "Live TV", "EPG fetch")
}

// XMLTV structures for parsing
type xmltvTV struct {
	XMLName    xml.Name         `xml:"tv"`
	Channels   []xmltvChannel   `xml:"channel"`
	Programmes []xmltvProgramme `xml:"programme"`
}

type xmltvChannel struct {
	ID          string      `xml:"id,attr"`
	DisplayName []xmltvLang `xml:"display-name"`
	Icon        []xmltvIcon `xml:"icon"`
}

type xmltvProgramme struct {
	Start    string         `xml:"start,attr"`
	Stop     string         `xml:"stop,attr"`
	Channel  string         `xml:"channel,attr"`
	Title    []xmltvLang    `xml:"title"`
	Desc     []xmltvLang    `xml:"desc"`
	Category []xmltvLang    `xml:"category"`
	EpNum    []xmltvEpisode `xml:"episode-num"`
	Icon     []xmltvIcon    `xml:"icon"`
	Rating   []xmltvRating  `xml:"rating"`
	// Presence-only airing flags (<new/>, <premiere>, <live/>, <previously-shown>).
	New             *struct{} `xml:"new"`
	Premiere        *struct{} `xml:"premiere"`
	Live            *struct{} `xml:"live"`
	PreviouslyShown *struct{} `xml:"previously-shown"`
}

type xmltvLang struct {
	Lang  string `xml:"lang,attr"`
	Value string `xml:",chardata"`
}

type xmltvIcon struct {
	Src string `xml:"src,attr"`
}

type xmltvEpisode struct {
	System string `xml:"system,attr"`
	Value  string `xml:",chardata"`
}

type xmltvRating struct {
	System string    `xml:"system,attr"`
	Value  xmltvLang `xml:"value"`
}

// parseXMLTV parses XMLTV data using streaming XML parser.
func (s *Service) parseXMLTV(reader io.Reader, schedule *models.EPGSchedule) error {
	decoder := xml.NewDecoder(reader)
	started := time.Now()
	channelCount := 0
	programCount := 0
	invalidTimeCount := 0

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("parse XML: %w", err)
		}

		if se, ok := token.(xml.StartElement); ok {
			switch se.Name.Local {
			case "channel":
				var ch xmltvChannel
				if err := decoder.DecodeElement(&ch, &se); err != nil {
					log.Printf("[epg] error parsing channel: %v", err)
					continue
				}
				// Normalize channel ID to lowercase to merge duplicates
				normalizedID := strings.ToLower(ch.ID)
				epgChannel := models.EPGChannel{
					ID:   normalizedID,
					Name: getFirstLangValue(ch.DisplayName),
				}
				for _, name := range ch.DisplayName {
					if strings.TrimSpace(name.Value) != "" {
						epgChannel.Aliases = append(epgChannel.Aliases, name.Value)
					}
				}
				if len(ch.Icon) > 0 {
					epgChannel.Icon = ch.Icon[0].Src
				}
				schedule.Channels[normalizedID] = epgChannel
				channelCount++

			case "programme":
				var prog xmltvProgramme
				if err := decoder.DecodeElement(&prog, &se); err != nil {
					log.Printf("[epg] error parsing programme: %v", err)
					continue
				}

				start, err := parseXMLTVTime(prog.Start)
				if err != nil {
					invalidTimeCount++
					if s.xmltvInvalidTimes != nil {
						s.xmltvInvalidTimes[strings.ToLower(prog.Channel)]++
					}
					continue
				}
				stop, err := parseXMLTVTime(prog.Stop)
				if err != nil {
					invalidTimeCount++
					if s.xmltvInvalidTimes != nil {
						s.xmltvInvalidTimes[strings.ToLower(prog.Channel)]++
					}
					continue
				}

				// Normalize channel ID to lowercase to merge duplicates
				normalizedChannelID := strings.ToLower(prog.Channel)
				epgProgram := models.EPGProgram{
					ChannelID:   normalizedChannelID,
					Title:       getFirstLangValue(prog.Title),
					Description: getFirstLangValue(prog.Desc),
					Start:       start,
					Stop:        stop,
				}

				// Parse categories
				for _, cat := range prog.Category {
					if cat.Value != "" {
						epgProgram.Categories = append(epgProgram.Categories, cat.Value)
					}
				}

				// Parse episode number
				for _, ep := range prog.EpNum {
					if ep.System == "onscreen" && ep.Value != "" {
						epgProgram.Episode = ep.Value
						break
					}
					if ep.System == "xmltv_ns" && ep.Value != "" {
						epgProgram.Episode = parseXMLTVNSEpisode(ep.Value)
					}
				}

				// Parse icon
				if len(prog.Icon) > 0 {
					epgProgram.Icon = prog.Icon[0].Src
				}

				// Parse rating
				if len(prog.Rating) > 0 {
					epgProgram.Rating = prog.Rating[0].Value.Value
				}

				epgProgram.New = prog.New != nil || prog.Premiere != nil || prog.Live != nil
				epgProgram.PreviouslyShown = prog.PreviouslyShown != nil

				schedule.Programs[normalizedChannelID] = append(schedule.Programs[normalizedChannelID], epgProgram)
				programCount++
			}
		}
	}

	// Sort programs by start time for each channel
	for channelID := range schedule.Programs {
		sort.Slice(schedule.Programs[channelID], func(i, j int) bool {
			return schedule.Programs[channelID][i].Start.Before(schedule.Programs[channelID][j].Start)
		})
	}

	log.Printf("[epg] XMLTV parser summary channels=%d programmes=%d invalidTimes=%d elapsed=%s",
		channelCount,
		programCount,
		invalidTimeCount,
		time.Since(started).Round(time.Millisecond),
	)

	return nil
}

// getFirstLangValue returns the first non-empty value from a slice of lang values.
func getFirstLangValue(values []xmltvLang) string {
	for _, v := range values {
		if v.Value != "" {
			return strings.TrimSpace(v.Value)
		}
	}
	return ""
}

// parseXMLTVTime parses XMLTV time format (YYYYMMDDHHmmss +/-HHMM).
var xmltvTimeRegex = regexp.MustCompile(`^(\d{14})(?:\s*([+-]\d{4}))?$`)

// Precompiled for normalizeChannelID, which is called (indirectly, via
// findProgramsByChannelMatch's linear scan) once per EPG channel on every unmatched
// lookup - recompiling these from scratch per call (as this used to do) made a single
// miss against a large EPG dataset cost tens of thousands of regexp.MustCompile calls.
var (
	countryPrefixRegex = regexp.MustCompile(`^[a-z]{2}\s*[\|\-]\s*`)
	countrySuffixRegex = regexp.MustCompile(`\.[a-z]{2}$`)
	nonAlphaNumRegex   = regexp.MustCompile(`[^a-z0-9]`)
)

func parseXMLTVTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	matches := xmltvTimeRegex.FindStringSubmatch(s)
	if matches == nil {
		return time.Time{}, fmt.Errorf("invalid XMLTV time format: %s", s)
	}

	dateStr := matches[1]
	tzStr := matches[2]

	var loc *time.Location = time.UTC
	if tzStr != "" {
		// Parse timezone offset
		sign := 1
		if tzStr[0] == '-' {
			sign = -1
		}
		hours := 0
		minutes := 0
		fmt.Sscanf(tzStr[1:], "%02d%02d", &hours, &minutes)
		offset := sign * (hours*3600 + minutes*60)
		loc = time.FixedZone(tzStr, offset)
	}

	t, err := time.ParseInLocation("20060102150405", dateStr, loc)
	if err != nil {
		return time.Time{}, err
	}

	return t.UTC(), nil
}

// parseXMLTVNSEpisode parses xmltv_ns episode format (season.episode.part) to human readable.
func parseXMLTVNSEpisode(s string) string {
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return s
	}

	season := 0
	episode := 0

	// Parse season (0-based in xmltv_ns)
	if parts[0] != "" {
		fmt.Sscanf(parts[0], "%d", &season)
		season++ // Convert from 0-based
	}

	// Parse episode (may have / for multi-part)
	if parts[1] != "" {
		epParts := strings.Split(parts[1], "/")
		fmt.Sscanf(epParts[0], "%d", &episode)
		episode++ // Convert from 0-based
	}

	if season > 0 && episode > 0 {
		return fmt.Sprintf("S%02dE%02d", season, episode)
	} else if episode > 0 {
		return fmt.Sprintf("E%02d", episode)
	}

	return s
}

// GetNowPlaying returns current and next programs for the specified channel IDs.
// The optional timeOffset shifts the effective "now" used for determining
// current/next programs (negative offset = look earlier in stored data).
func (s *Service) GetNowPlaying(channelIDs []string, timeOffset ...time.Duration) []models.EPGNowPlaying {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now().UTC()
	if len(timeOffset) > 0 {
		now = now.Add(timeOffset[0])
	}
	result := make([]models.EPGNowPlaying, 0, len(channelIDs))

	// Build normalized lookup indexes once for the entire batch. The previous fallback
	// linearly scanned the full EPG for every unmatched playlist channel, turning large
	// sports lookups into O(playlist × EPG). Ambiguous normalized keys are deliberately
	// omitted rather than silently binding a channel to the wrong guide entry.
	byNormalizedID := make(map[string][]models.EPGProgram, len(s.schedule.Programs))
	ambiguousIDs := make(map[string]struct{})
	for epgChannelID, programs := range s.schedule.Programs {
		key := normalizeChannelID(epgChannelID)
		if _, exists := byNormalizedID[key]; exists {
			delete(byNormalizedID, key)
			ambiguousIDs[key] = struct{}{}
		} else if _, ambiguous := ambiguousIDs[key]; !ambiguous {
			byNormalizedID[key] = programs
		}
	}
	byNormalizedName := make(map[string]string, len(s.schedule.Channels))
	ambiguousNames := make(map[string]struct{})
	for epgChannelID, channel := range s.schedule.Channels {
		programs := s.schedule.Programs[epgChannelID]
		if len(programs) == 0 {
			continue
		}
		// HDHomeRun XMLTV commonly lists the number first and the callsign in
		// another display-name. Repeated aliases of the same station are safe;
		// aliases shared by different stations must remain unresolved.
		for _, name := range append([]string{channel.Name}, channel.Aliases...) {
			key := normalizeChannelID(name)
			if key == "" {
				continue
			}
			if existingID, exists := byNormalizedName[key]; exists && existingID != epgChannelID {
				delete(byNormalizedName, key)
				ambiguousNames[key] = struct{}{}
			} else if _, ambiguous := ambiguousNames[key]; !ambiguous {
				byNormalizedName[key] = epgChannelID
			}
		}
	}

	for _, channelID := range channelIDs {
		np := models.EPGNowPlaying{ChannelID: channelID}

		// Normalize channel ID to lowercase for lookup (EPG data is stored lowercase)
		lookupID := strings.ToLower(channelID)

		// Try to find programs with normalized channel ID
		programs := s.schedule.Programs[lookupID]

		// If no direct match, use the per-batch normalized ID/name indexes.
		if len(programs) == 0 {
			key := normalizeChannelID(channelID)
			programs = byNormalizedID[key]
			if len(programs) == 0 {
				if matchedID, ok := s.exactNameIndex[exactChannelName(channelID)]; ok {
					programs = s.schedule.Programs[matchedID]
				} else if matchedID, ok := byNormalizedName[key]; ok {
					programs = s.schedule.Programs[matchedID]
				}
			}
		}

		for i, prog := range programs {
			// Check if this is the current program
			if prog.Start.Before(now) && prog.Stop.After(now) {
				np.Current = &programs[i]
				// Get next program
				if i+1 < len(programs) {
					np.Next = &programs[i+1]
				}
				break
			}
			// Check if this is the next upcoming program
			if prog.Start.After(now) && np.Current == nil {
				np.Next = &programs[i]
				break
			}
		}

		result = append(result, np)
	}

	return result
}

// GetSchedule returns programs for a channel within a time range.
func (s *Service) GetSchedule(channelID string, start, end time.Time) []models.EPGProgram {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Normalize channel ID to lowercase for lookup (EPG data is stored lowercase)
	lookupID := strings.ToLower(channelID)

	// Try to find programs with normalized channel ID
	programs := s.schedule.Programs[lookupID]

	// If no match, try to find by other matching strategies
	if len(programs) == 0 {
		programs = s.findProgramsByChannelMatch(channelID)
	}

	var result []models.EPGProgram
	for _, prog := range programs {
		// Include programs that overlap with the time range
		if prog.Stop.After(start) && prog.Start.Before(end) {
			result = append(result, prog)
		}
	}

	return result
}

// GetScheduleMultiple returns programs for multiple channels within a time range.
// This is optimized for the EPG grid view by fetching all data in a single call.
func (s *Service) GetScheduleMultiple(channelIDs []string, start, end time.Time) map[string][]models.EPGProgram {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]models.EPGProgram, len(channelIDs))

	for _, channelID := range channelIDs {
		// Normalize channel ID to lowercase for lookup (EPG data is stored lowercase)
		lookupID := strings.ToLower(channelID)

		// Try to find programs with normalized channel ID
		programs := s.schedule.Programs[lookupID]

		// If no match, try to find by other matching strategies
		if len(programs) == 0 {
			programs = s.findProgramsByChannelMatch(channelID)
		}

		var channelPrograms []models.EPGProgram
		for _, prog := range programs {
			// Include programs that overlap with the time range
			if prog.Stop.After(start) && prog.Start.Before(end) {
				channelPrograms = append(channelPrograms, prog)
			}
		}

		result[channelID] = channelPrograms
	}

	return result
}

// GetChannelSchedule returns the full day schedule for a channel.
func (s *Service) GetChannelSchedule(channelID string, date time.Time) []models.EPGProgram {
	// Get start and end of the day in UTC
	year, month, day := date.Date()
	start := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	return s.GetSchedule(channelID, start, end)
}

// findProgramsByChannelMatch tries to match a channel ID using various strategies.
func (s *Service) findProgramsByChannelMatch(channelID string) []models.EPGProgram {
	normalizedInput := normalizeChannelID(channelID)

	// O(1): normalized-ID and normalized-name indexes, built once whenever the schedule is
	// (re)assigned (rebuildScheduleIndexLocked) instead of scanned per call.
	if epgChannelID, ok := s.normalizedIDIndex[normalizedInput]; ok {
		return s.schedule.Programs[epgChannelID]
	}
	if epgChannelID, ok := s.exactNameIndex[exactChannelName(channelID)]; ok {
		return s.schedule.Programs[epgChannelID]
	}
	if epgChannelID, ok := s.normalizedNameIndex[normalizedInput]; ok {
		return s.schedule.Programs[epgChannelID]
	}

	// No substring fallback: with numeric tvg-ids (Dispatcharr's default) "12" contains
	// "1", so a whole category bound to the first guide channels' programmes.
	return nil
}

// normalizeChannelID normalizes a channel ID/name for comparison.
func normalizeChannelID(s string) string {
	// Convert to lowercase
	s = strings.ToLower(s)
	// Remove common suffixes (with or without space)
	suffixes := []string{" hd", " sd", " fhd", " uhd", " 4k", "hd", "sd", "fhd", "uhd", "4k"}
	for _, suffix := range suffixes {
		s = strings.TrimSuffix(s, suffix)
	}
	// Remove country prefixes like "us |", "uk |", "ca -" etc
	s = countryPrefixRegex.ReplaceAllString(s, "")
	// Remove trailing country codes like .us, .uk, .ca
	s = countrySuffixRegex.ReplaceAllString(s, "")
	// Remove special characters and spaces
	return nonAlphaNumRegex.ReplaceAllString(s, "")
}

// GetEPGChannelID attempts to find the EPG channel ID for a live channel.
func (s *Service) GetEPGChannelID(tvgID, channelName string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// EPG data is stored with lowercase channel IDs, so normalize for lookup
	if tvgID != "" {
		lookupID := strings.ToLower(tvgID)
		if _, exists := s.schedule.Channels[lookupID]; exists {
			return lookupID
		}
	}

	// Match by normalized channel name
	if channelName != "" {
		normalizedName := normalizeChannelID(channelName)
		for id, ch := range s.schedule.Channels {
			if normalizeChannelID(id) == normalizedName || normalizeChannelID(ch.Name) == normalizedName {
				return id
			}
		}
	}

	return ""
}

// GetAllChannels returns all EPG channels.
func (s *Service) GetAllChannels() map[string]models.EPGChannel {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to avoid concurrent modification
	result := make(map[string]models.EPGChannel, len(s.schedule.Channels))
	for k, v := range s.schedule.Channels {
		result[k] = v
	}
	return result
}

// ChannelIcons returns guide icons keyed by the requested channel IDs. Lookup
// follows the guide's own matching (exact ID, then unambiguous normalized ID or
// display-name alias), so tuner lineups keyed by callsign get their station logo.
func (s *Service) ChannelIcons(channelIDs []string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string)
	for _, channelID := range channelIDs {
		epgChannelID := strings.ToLower(channelID)
		if _, ok := s.schedule.Channels[epgChannelID]; !ok {
			key := normalizeChannelID(channelID)
			if matched, ok := s.normalizedIDIndex[key]; ok {
				epgChannelID = matched
			} else if matched, ok := s.exactNameIndex[exactChannelName(channelID)]; ok {
				epgChannelID = matched
			} else if matched, ok := s.normalizedNameIndex[key]; ok {
				epgChannelID = matched
			}
		}
		if icon := strings.TrimSpace(s.schedule.Channels[epgChannelID].Icon); icon != "" {
			result[channelID] = icon
		}
	}
	return result
}

// saveToDisk persists the EPG data to disk.
func (s *Service) saveToDisk() error {
	s.mu.RLock()
	data, err := json.Marshal(s.schedule)
	s.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("marshal EPG data: %w", err)
	}

	cacheDir := filepath.Join(s.storageDir, "epg")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	cachePath := filepath.Join(cacheDir, epgCacheFile)
	tmpPath := cachePath + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// readFromDisk loads EPG data without mutating the active schedule.
func (s *Service) readFromDisk() (*models.EPGSchedule, error) {
	cachePath := filepath.Join(s.storageDir, "epg", epgCacheFile)

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}

	var schedule models.EPGSchedule
	if err := json.Unmarshal(data, &schedule); err != nil {
		return nil, fmt.Errorf("unmarshal EPG data: %w", err)
	}
	return &schedule, nil
}

// IsEnabled returns whether EPG is enabled in settings.
func (s *Service) IsEnabled() bool {
	settings, err := s.cfgManager.Load()
	if err != nil {
		return false
	}
	return isEPGRefreshEnabled(settings) || len(s.hdHomeRunSources(settings)) > 0
}
