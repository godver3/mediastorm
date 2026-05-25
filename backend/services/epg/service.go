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

	mu         sync.RWMutex
	schedule   *models.EPGSchedule
	refreshing bool
	lastError  string
}

type epgXMLTVSource struct {
	name     string
	url      string
	proxyURL string
	priority int
}

type epgXtreamSource struct {
	name     string
	settings config.Settings
}

// NewService creates a new EPG service.
func NewService(storageDir string, cfgManager *config.Manager) *Service {
	s := &Service{
		cfgManager: cfgManager,
		storageDir: storageDir,
		client: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		schedule: &models.EPGSchedule{
			Channels: make(map[string]models.EPGChannel),
			Programs: make(map[string][]models.EPGProgram),
		},
	}

	// Ensure cache directory exists
	cacheDir := filepath.Join(storageDir, "epg")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[epg] failed to create cache directory: %v", err)
	}

	// Load cached EPG data on startup
	if err := s.loadFromDisk(); err != nil {
		log.Printf("[epg] no cached EPG data found or error loading: %v", err)
	} else {
		log.Printf("[epg] loaded cached EPG data: %d channels, %d programs",
			len(s.schedule.Channels), s.countPrograms())
	}

	return s
}

// countPrograms returns total number of programs across all channels.
func (s *Service) countPrograms() int {
	count := 0
	for _, progs := range s.schedule.Programs {
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
		Enabled:      settings.Live.EPG.Enabled,
		ChannelCount: len(s.schedule.Channels),
		ProgramCount: s.countPrograms(),
		Refreshing:   s.refreshing,
		LastError:    s.lastError,
		SourceCount:  countConfiguredXMLTVSources(settings),
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

	if !isEPGRefreshEnabled(settings) {
		return errors.New("EPG is disabled")
	}

	// Create new schedule
	newSchedule := &models.EPGSchedule{
		Channels:    make(map[string]models.EPGChannel),
		Programs:    make(map[string][]models.EPGProgram),
		LastUpdated: time.Now().UTC(),
	}

	for _, source := range xtreamSources {
		sourceSettings := source.settings
		log.Printf("[epg] fetching Xtream EPG source name=%q proxyConfigured=%v", source.name, strings.TrimSpace(sourceSettings.Live.ProxyURL) != "")
		if err := s.fetchXtreamEPG(ctx, &sourceSettings, newSchedule); err != nil {
			log.Printf("[epg] Xtream EPG fetch failed for source name=%q: %v", source.name, err)
			s.mu.Lock()
			s.lastError = fmt.Sprintf("%s Xtream EPG: %v", source.name, err)
			s.mu.Unlock()
		} else {
			newSchedule.SourceType = "xtream"
		}
	}

	xmltvSources := collectXMLTVSources(settings)
	for _, source := range xmltvSources {
		log.Printf("[epg] fetching XMLTV source name=%q proxyConfigured=%v", source.name, strings.TrimSpace(source.proxyURL) != "")
		beforeChannels := len(newSchedule.Channels)
		beforePrograms := 0
		for _, programs := range newSchedule.Programs {
			beforePrograms += len(programs)
		}
		if err := s.fetchXMLTVWithProxy(ctx, source.url, source.proxyURL, newSchedule); err != nil {
			log.Printf("[epg] failed to fetch XMLTV source name=%q: %v", source.name, err)
			s.mu.Lock()
			s.lastError = fmt.Sprintf("%s: %v", source.name, err)
			s.mu.Unlock()
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
		}
	}

	// Prune old programs based on retention
	retentionDays := settings.Live.EPG.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 7
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	futureLimit := time.Now().Add(time.Duration(retentionDays) * 24 * time.Hour)

	// When a time offset is configured, programs are shifted at display time.
	// Extend both cutoffs by the absolute offset to avoid premature pruning.
	if offsetMin := settings.Live.EPG.TimeOffsetMinutes; offsetMin != 0 {
		abs := time.Duration(offsetMin) * time.Minute
		if abs < 0 {
			abs = -abs
		}
		cutoff = cutoff.Add(-abs)
		futureLimit = futureLimit.Add(abs)
	}

	for channelID, programs := range newSchedule.Programs {
		var filtered []models.EPGProgram
		for _, prog := range programs {
			if prog.Stop.After(cutoff) && prog.Start.Before(futureLimit) {
				filtered = append(filtered, prog)
			}
		}
		newSchedule.Programs[channelID] = filtered
	}

	// Update the schedule
	s.mu.Lock()
	s.schedule = newSchedule
	s.mu.Unlock()

	// Save to disk
	if err := s.saveToDisk(); err != nil {
		log.Printf("[epg] failed to save EPG to disk: %v", err)
	}

	log.Printf("[epg] refresh complete: %d channels, %d programs",
		len(newSchedule.Channels), s.countPrograms())

	return nil
}

// fetchXtreamEPG fetches EPG data from the Xtream Codes xmltv.php endpoint.
func (s *Service) fetchXtreamEPG(ctx context.Context, settings *config.Settings, schedule *models.EPGSchedule) error {
	host := strings.TrimRight(settings.Live.XtreamHost, "/")
	username := settings.Live.XtreamUsername
	password := settings.Live.XtreamPassword

	epgURL := fmt.Sprintf("%s/xmltv.php?username=%s&password=%s",
		host, url.QueryEscape(username), url.QueryEscape(password))

	return s.fetchXMLTV(ctx, epgURL, schedule)
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

	resp, err := s.httpClient(proxyURL).Do(req)
	if err != nil {
		log.Printf("[epg] XMLTV HTTP request failed host=%q elapsed=%s error=%v", hostLabel, time.Since(started).Round(time.Millisecond), err)
		return fmt.Errorf("fetch EPG: %w", err)
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
		return fmt.Errorf("EPG fetch returned status %d", resp.StatusCode)
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
	return client
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
					continue
				}
				stop, err := parseXMLTVTime(prog.Stop)
				if err != nil {
					invalidTimeCount++
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

	for _, channelID := range channelIDs {
		np := models.EPGNowPlaying{ChannelID: channelID}

		// Normalize channel ID to lowercase for lookup (EPG data is stored lowercase)
		lookupID := strings.ToLower(channelID)

		// Try to find programs with normalized channel ID
		programs := s.schedule.Programs[lookupID]

		// If no match, try to find by other matching strategies
		if len(programs) == 0 {
			programs = s.findProgramsByChannelMatch(channelID)
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
	// Normalize the input channel ID
	normalizedInput := normalizeChannelID(channelID)

	// Try to find a matching channel by normalized ID
	for epgChannelID, programs := range s.schedule.Programs {
		if normalizeChannelID(epgChannelID) == normalizedInput {
			return programs
		}
	}

	// Try matching by channel name
	for epgChannelID, ch := range s.schedule.Channels {
		if normalizeChannelID(ch.Name) == normalizedInput {
			if programs := s.schedule.Programs[epgChannelID]; len(programs) > 0 {
				return programs
			}
		}
	}

	// Try partial matching - check if input contains or is contained by EPG channel ID
	for epgChannelID, programs := range s.schedule.Programs {
		epgNorm := normalizeChannelID(epgChannelID)
		if strings.Contains(epgNorm, normalizedInput) || strings.Contains(normalizedInput, epgNorm) {
			if len(programs) > 0 {
				return programs
			}
		}
	}

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
	prefixPattern := regexp.MustCompile(`^[a-z]{2}\s*[\|\-]\s*`)
	s = prefixPattern.ReplaceAllString(s, "")
	// Remove trailing country codes like .us, .uk, .ca
	s = regexp.MustCompile(`\.[a-z]{2}$`).ReplaceAllString(s, "")
	// Remove special characters and spaces
	reg := regexp.MustCompile(`[^a-z0-9]`)
	return reg.ReplaceAllString(s, "")
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

// loadFromDisk loads the EPG data from disk.
func (s *Service) loadFromDisk() error {
	cachePath := filepath.Join(s.storageDir, "epg", epgCacheFile)

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return err
	}

	var schedule models.EPGSchedule
	if err := json.Unmarshal(data, &schedule); err != nil {
		return fmt.Errorf("unmarshal EPG data: %w", err)
	}

	s.mu.Lock()
	s.schedule = &schedule
	s.mu.Unlock()

	return nil
}

// IsEnabled returns whether EPG is enabled in settings.
func (s *Service) IsEnabled() bool {
	settings, err := s.cfgManager.Load()
	if err != nil {
		return false
	}
	return settings.Live.EPG.Enabled
}
