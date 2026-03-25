package localmedia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"novastream/internal/datastore"
	"novastream/models"
	"novastream/utils/parsett"
)

var (
	ErrLibraryNotFound   = errors.New("local media library not found")
	ErrLibraryScanning   = errors.New("local media library scan already in progress")
	ErrLibraryNameNeeded = errors.New("library name is required")
	ErrLibraryPathNeeded = errors.New("library root path is required")
	ErrLibraryTypeNeeded = errors.New("library type is required")
	ErrItemNotFound      = errors.New("local media item not found")
)

type metadataMatcher interface {
	Search(ctx context.Context, query string, mediaType string) ([]models.SearchResult, error)
	MovieDetails(ctx context.Context, query models.MovieDetailsQuery) (*models.Title, error)
	SeriesDetails(ctx context.Context, query models.SeriesDetailsQuery) (*models.SeriesDetails, error)
}

type scanState struct {
	inProgress bool
	startedAt  time.Time
}

type scanFileCandidate struct {
	path         string
	relativePath string
	sizeBytes    int64
}

type Service struct {
	store       *datastore.DataStore
	repo        datastore.LocalMediaRepository
	metadata    metadataMatcher
	ffprobePath string

	mu    sync.Mutex
	scans map[string]scanState
}

type scanMetadataCache struct {
	mu      sync.Mutex
	details map[string]*models.Title
}

const parseBatchSize = 200

func NewService(store *datastore.DataStore, metadata metadataMatcher, ffprobePath string) (*Service, error) {
	if store == nil {
		return nil, errors.New("local media datastore is required")
	}
	path := strings.TrimSpace(ffprobePath)
	if path == "" {
		path = "ffprobe"
	}
	service := &Service{
		store:       store,
		repo:        store.LocalMedia(),
		metadata:    metadata,
		ffprobePath: path,
		scans:       make(map[string]scanState),
	}
	if err := service.reconcileScanState(context.Background()); err != nil {
		log.Printf("[localmedia] failed to reconcile scan state on startup: %v", err)
	}
	return service, nil
}

func (s *Service) reconcileScanState(ctx context.Context) error {
	libraries, err := s.repo.ListLibraries(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range libraries {
		library := libraries[i]
		if library.LastScanStatus != models.LocalMediaScanStatusScanning {
			continue
		}
		library.LastScanStatus = models.LocalMediaScanStatusFailed
		library.LastScanError = "scan interrupted by restart"
		library.LastScanFinishedAt = &now
		library.UpdatedAt = now
		if err := s.repo.UpdateLibrary(ctx, &library); err != nil {
			log.Printf("[localmedia] failed to reconcile stale scanning library=%q id=%s: %v", library.Name, library.ID, err)
			continue
		}
		log.Printf("[localmedia] reconciled stale scan state library=%q id=%s", library.Name, library.ID)
	}
	return nil
}

func (s *Service) ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error) {
	return s.repo.ListLibraries(ctx)
}

func (s *Service) CreateLibrary(ctx context.Context, input models.LocalMediaLibraryCreateInput) (*models.LocalMediaLibrary, error) {
	name, rootPath, filterOutTerms, minFileSizeBytes, err := validateLocalMediaLibraryInput(input)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	library := &models.LocalMediaLibrary{
		ID:               uuid.NewString(),
		Name:             name,
		Type:             input.Type,
		RootPath:         rootPath,
		FilterOutTerms:   filterOutTerms,
		MinFileSizeBytes: minFileSizeBytes,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastScanStatus:   models.LocalMediaScanStatusIdle,
	}
	if err := s.repo.CreateLibrary(ctx, library); err != nil {
		return nil, err
	}
	return library, nil
}

func (s *Service) UpdateLibrary(ctx context.Context, libraryID string, input models.LocalMediaLibraryCreateInput) (*models.LocalMediaLibrary, error) {
	if strings.TrimSpace(libraryID) == "" {
		return nil, ErrLibraryNotFound
	}

	library, err := s.repo.GetLibrary(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	if library == nil {
		return nil, ErrLibraryNotFound
	}
	if library.LastScanStatus == models.LocalMediaScanStatusScanning {
		return nil, ErrLibraryScanning
	}

	name, rootPath, filterOutTerms, minFileSizeBytes, err := validateLocalMediaLibraryInput(input)
	if err != nil {
		return nil, err
	}

	library.Name = name
	library.Type = input.Type
	library.RootPath = rootPath
	library.FilterOutTerms = filterOutTerms
	library.MinFileSizeBytes = minFileSizeBytes
	library.UpdatedAt = time.Now().UTC()

	if err := s.repo.UpdateLibrary(ctx, library); err != nil {
		return nil, err
	}
	return library, nil
}

func (s *Service) SearchMetadata(ctx context.Context, query, mediaType string) ([]models.SearchResult, error) {
	if s.metadata == nil {
		return nil, errors.New("metadata service unavailable")
	}
	return s.metadata.Search(ctx, query, mediaType)
}

func (s *Service) BrowseDirectories(path string) (*models.LocalMediaDirectoryListing, error) {
	currentPath := strings.TrimSpace(path)
	if currentPath == "" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			currentPath = home
		} else {
			currentPath = string(filepath.Separator)
		}
	}
	currentPath = filepath.Clean(currentPath)

	info, err := os.Stat(currentPath)
	if err != nil {
		return nil, fmt.Errorf("stat browse path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", currentPath)
	}

	dirEntries, err := os.ReadDir(currentPath)
	if err != nil {
		return nil, fmt.Errorf("read browse path: %w", err)
	}

	entries := make([]models.LocalMediaDirectoryEntry, 0, len(dirEntries))
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		entries = append(entries, models.LocalMediaDirectoryEntry{
			Name: name,
			Path: filepath.Join(currentPath, name),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	parentPath := filepath.Dir(currentPath)
	if parentPath == "." || parentPath == currentPath {
		parentPath = ""
	}

	return &models.LocalMediaDirectoryListing{
		CurrentPath: currentPath,
		ParentPath:  parentPath,
		Entries:     entries,
	}, nil
}

func (s *Service) DeleteLibrary(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrLibraryNotFound
	}
	return s.repo.DeleteLibrary(ctx, id)
}

func (s *Service) ListItems(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaItemListResult, error) {
	query.Filter = strings.TrimSpace(query.Filter)
	switch query.Filter {
	case "", "all", string(models.LocalMediaMatchStatusMatched), string(models.LocalMediaMatchStatusLowConfidence), string(models.LocalMediaMatchStatusUnmatched), string(models.LocalMediaMatchStatusManual):
	default:
		query.Filter = "all"
	}

	query.Sort = strings.TrimSpace(query.Sort)
	switch query.Sort {
	case "", "updated", "name", "confidence", "year", "size", "modified", "status":
	default:
		query.Sort = "updated"
	}

	query.Dir = strings.TrimSpace(query.Dir)
	if query.Dir != "asc" && query.Dir != "desc" {
		query.Dir = "desc"
	}

	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 50
	}
	if query.Offset < 0 {
		query.Offset = 0
	}

	result, err := s.repo.ListItemsByLibrary(ctx, libraryID, query)
	if err != nil {
		return nil, err
	}
	hydrateLocalMediaItemResultExternalIDs(result)
	s.enrichEpisodeMetadata(ctx, result.Items)
	return result, nil
}

func (s *Service) ListGroups(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaGroupListResult, error) {
	query.Filter = strings.TrimSpace(query.Filter)
	switch query.Filter {
	case "", "all", string(models.LocalMediaMatchStatusMatched), string(models.LocalMediaMatchStatusLowConfidence), string(models.LocalMediaMatchStatusUnmatched), string(models.LocalMediaMatchStatusManual):
	default:
		query.Filter = "all"
	}

	query.Sort = strings.TrimSpace(query.Sort)
	switch query.Sort {
	case "", "updated", "name", "confidence", "year", "size", "modified", "status":
	default:
		query.Sort = "updated"
	}

	query.Dir = strings.TrimSpace(query.Dir)
	if query.Dir != "asc" && query.Dir != "desc" {
		query.Dir = "desc"
	}

	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 50
	}
	if query.Offset < 0 {
		query.Offset = 0
	}

	items, err := s.repo.ListAllItemsByLibrary(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	result := &models.LocalMediaItemListResult{Items: items}
	hydrateLocalMediaItemResultExternalIDs(result)
	s.enrichEpisodeMetadata(ctx, result.Items)

	filtered := make([]models.LocalMediaItem, 0, len(result.Items))
	for _, item := range result.Items {
		if localMediaItemMatchesQuery(item, query.Filter, query.Query) {
			filtered = append(filtered, item)
		}
	}

	groups := buildLocalMediaGroups(filtered)
	sortLocalMediaGroups(groups, query.Sort, query.Dir)

	total := len(groups)
	start := min(query.Offset, total)
	end := min(start+query.Limit, total)
	if start < end {
		groups = groups[start:end]
	} else {
		groups = []models.LocalMediaItemGroup{}
	}

	return &models.LocalMediaGroupListResult{
		Groups: groups,
		Total:  total,
		Limit:  query.Limit,
		Offset: query.Offset,
	}, nil
}

type localMediaGroupAccumulator struct {
	group   models.LocalMediaItemGroup
	seasons map[int]*localMediaSeasonAccumulator
}

type localMediaSeasonAccumulator struct {
	season   models.LocalMediaSeasonGroup
	episodes map[string]*localMediaEpisodeAccumulator
}

type localMediaEpisodeAccumulator struct {
	episode models.LocalMediaEpisodeGroup
}

func localMediaItemMatchesQuery(item models.LocalMediaItem, filter, rawQuery string) bool {
	if filter != "" && filter != "all" && string(item.MatchStatus) != filter {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(rawQuery))
	if query == "" {
		return true
	}
	metaName := ""
	if item.Metadata != nil {
		metaName = item.Metadata.Name
	}
	fields := []string{
		item.RelativePath,
		item.FileName,
		item.DetectedTitle,
		item.MatchedName,
		metaName,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func buildLocalMediaGroups(items []models.LocalMediaItem) []models.LocalMediaItemGroup {
	acc := make(map[string]*localMediaGroupAccumulator)
	order := make([]string, 0)

	for _, item := range items {
		key, groupType := localMediaGroupKey(item)
		entry, ok := acc[key]
		if !ok {
			entry = &localMediaGroupAccumulator{
				group: models.LocalMediaItemGroup{
					ID:          key,
					GroupType:   groupType,
					LibraryType: item.LibraryType,
				},
				seasons: make(map[int]*localMediaSeasonAccumulator),
			}
			acc[key] = entry
			order = append(order, key)
		}
		updateLocalMediaGroupSummary(&entry.group, item)
		if item.LibraryType == models.LocalMediaLibraryTypeShow {
			seasonNumber := item.SeasonNumber
			seasonEntry, ok := entry.seasons[seasonNumber]
			if !ok {
				seasonEntry = &localMediaSeasonAccumulator{
					season: models.LocalMediaSeasonGroup{
						ID:           fmt.Sprintf("%s:season:%d", key, seasonNumber),
						SeasonNumber: seasonNumber,
					},
					episodes: make(map[string]*localMediaEpisodeAccumulator),
				}
				entry.seasons[seasonNumber] = seasonEntry
			}
			updateLocalMediaSeasonSummary(&seasonEntry.season, item)
			episodeKey := localMediaEpisodeGroupKey(item)
			episodeEntry, ok := seasonEntry.episodes[episodeKey]
			if !ok {
				episodeEntry = &localMediaEpisodeAccumulator{
					episode: models.LocalMediaEpisodeGroup{
						ID:              fmt.Sprintf("%s:episode:%s", seasonEntry.season.ID, episodeKey),
						EpisodeNumber:   item.EpisodeNumber,
						EpisodeTitle:    strings.TrimSpace(item.EpisodeTitle),
						EpisodeOverview: strings.TrimSpace(item.EpisodeOverview),
						EpisodeImage:    item.EpisodeImage,
					},
				}
				seasonEntry.episodes[episodeKey] = episodeEntry
			}
			updateLocalMediaEpisodeSummary(&episodeEntry.episode, item)
			episodeEntry.episode.Items = append(episodeEntry.episode.Items, item)
			continue
		}
		entry.group.Items = append(entry.group.Items, item)
	}

	groups := make([]models.LocalMediaItemGroup, 0, len(order))
	for _, key := range order {
		entry := acc[key]
		group := entry.group
		if group.LibraryType == models.LocalMediaLibraryTypeShow {
			seasonNumbers := make([]int, 0, len(entry.seasons))
			for seasonNumber := range entry.seasons {
				seasonNumbers = append(seasonNumbers, seasonNumber)
			}
			sort.Ints(seasonNumbers)
			seasons := make([]models.LocalMediaSeasonGroup, 0, len(seasonNumbers))
			for _, seasonNumber := range seasonNumbers {
				season := entry.seasons[seasonNumber].season
				episodeKeys := make([]string, 0, len(entry.seasons[seasonNumber].episodes))
				for episodeKey := range entry.seasons[seasonNumber].episodes {
					episodeKeys = append(episodeKeys, episodeKey)
				}
				sort.Strings(episodeKeys)
				episodes := make([]models.LocalMediaEpisodeGroup, 0, len(episodeKeys))
				season.ItemCount = 0
				for _, episodeKey := range episodeKeys {
					episode := entry.seasons[seasonNumber].episodes[episodeKey].episode
					sortLocalMediaItems(episode.Items, "quality", "desc")
					episode.ItemCount = len(episode.Items)
					season.ItemCount += episode.ItemCount
					episodes = append(episodes, episode)
				}
				sortLocalMediaEpisodeGroups(episodes)
				season.Episodes = episodes
				seasons = append(seasons, season)
			}
			group.Seasons = seasons
			group.ItemCount = 0
			for _, season := range seasons {
				group.ItemCount += season.ItemCount
			}
		} else {
			sortLocalMediaItems(group.Items, "quality", "desc")
			group.ItemCount = len(group.Items)
		}
		groups = append(groups, group)
	}
	return groups
}

func localMediaGroupKey(item models.LocalMediaItem) (string, string) {
	switch item.LibraryType {
	case models.LocalMediaLibraryTypeShow:
		if id := localMediaResolvedTitleID(item); id != "" {
			return "series:" + id, "series"
		}
		return fmt.Sprintf("series:%s:%d", normalizeLocalMediaGroupText(localMediaResolvedTitle(item)), localMediaResolvedYear(item)), "series"
	case models.LocalMediaLibraryTypeMovie:
		if id := localMediaResolvedTitleID(item); id != "" {
			return "movie:" + id, "movie_versions"
		}
		return fmt.Sprintf("movie:%s:%d", normalizeLocalMediaGroupText(localMediaResolvedTitle(item)), localMediaResolvedYear(item)), "movie_versions"
	default:
		return "file:" + item.ID, "file"
	}
}

func updateLocalMediaGroupSummary(group *models.LocalMediaItemGroup, item models.LocalMediaItem) {
	if group.Title == "" {
		group.Title = localMediaResolvedTitle(item)
	}
	if group.Overview == "" && item.Metadata != nil && strings.TrimSpace(item.Metadata.Overview) != "" {
		group.Overview = strings.TrimSpace(item.Metadata.Overview)
	}
	if group.Year == 0 {
		group.Year = localMediaResolvedYear(item)
	}
	if group.Poster == nil && item.Metadata != nil && item.Metadata.Poster != nil {
		group.Poster = item.Metadata.Poster
	}
	if group.TextPoster == nil && item.Metadata != nil && item.Metadata.TextPoster != nil {
		group.TextPoster = item.Metadata.TextPoster
	}
	if group.MatchStatus == "" || localMediaMatchStatusRank(item.MatchStatus) > localMediaMatchStatusRank(group.MatchStatus) {
		group.MatchStatus = item.MatchStatus
	}
	if item.IsMissing {
		group.MissingCount++
	}
	group.TotalSizeBytes += item.SizeBytes
	if group.ConfidenceMin == 0 || item.Confidence < group.ConfidenceMin {
		group.ConfidenceMin = item.Confidence
	}
	if item.Confidence > group.ConfidenceMax {
		group.ConfidenceMax = item.Confidence
	}
	group.LatestModifiedAt = latestTimePtr(group.LatestModifiedAt, item.ModifiedAt)
	group.LatestUpdatedAt = latestTimePtr(group.LatestUpdatedAt, &item.UpdatedAt)
}

func updateLocalMediaSeasonSummary(season *models.LocalMediaSeasonGroup, item models.LocalMediaItem) {
	if season.MatchStatus == "" || localMediaMatchStatusRank(item.MatchStatus) > localMediaMatchStatusRank(season.MatchStatus) {
		season.MatchStatus = item.MatchStatus
	}
	if item.IsMissing {
		season.MissingCount++
	}
	season.TotalSizeBytes += item.SizeBytes
	if season.ConfidenceMin == 0 || item.Confidence < season.ConfidenceMin {
		season.ConfidenceMin = item.Confidence
	}
	if item.Confidence > season.ConfidenceMax {
		season.ConfidenceMax = item.Confidence
	}
	season.LatestModifiedAt = latestTimePtr(season.LatestModifiedAt, item.ModifiedAt)
	season.LatestUpdatedAt = latestTimePtr(season.LatestUpdatedAt, &item.UpdatedAt)
}

func updateLocalMediaEpisodeSummary(episode *models.LocalMediaEpisodeGroup, item models.LocalMediaItem) {
	if episode.EpisodeTitle == "" && strings.TrimSpace(item.EpisodeTitle) != "" {
		episode.EpisodeTitle = strings.TrimSpace(item.EpisodeTitle)
	}
	if episode.EpisodeOverview == "" && strings.TrimSpace(item.EpisodeOverview) != "" {
		episode.EpisodeOverview = strings.TrimSpace(item.EpisodeOverview)
	}
	if episode.EpisodeImage == nil && item.EpisodeImage != nil {
		episode.EpisodeImage = item.EpisodeImage
	}
	if episode.MatchStatus == "" || localMediaMatchStatusRank(item.MatchStatus) > localMediaMatchStatusRank(episode.MatchStatus) {
		episode.MatchStatus = item.MatchStatus
	}
	if item.IsMissing {
		episode.MissingCount++
	}
	episode.TotalSizeBytes += item.SizeBytes
	if episode.ConfidenceMin == 0 || item.Confidence < episode.ConfidenceMin {
		episode.ConfidenceMin = item.Confidence
	}
	if item.Confidence > episode.ConfidenceMax {
		episode.ConfidenceMax = item.Confidence
	}
	episode.LatestModifiedAt = latestTimePtr(episode.LatestModifiedAt, item.ModifiedAt)
	episode.LatestUpdatedAt = latestTimePtr(episode.LatestUpdatedAt, &item.UpdatedAt)
}

func sortLocalMediaGroups(groups []models.LocalMediaItemGroup, sortBy, dir string) {
	desc := dir != "asc"
	sort.SliceStable(groups, func(i, j int) bool {
		a := groups[i]
		b := groups[j]
		cmp := compareLocalMediaGroups(a, b, sortBy)
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		}
		if cmp == 0 {
			cmp = strings.Compare(a.ID, b.ID)
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func compareLocalMediaGroups(a, b models.LocalMediaItemGroup, sortBy string) int {
	switch sortBy {
	case "name":
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case "confidence":
		return compareFloat64(a.ConfidenceMax, b.ConfidenceMax)
	case "year":
		return compareInt(a.Year, b.Year)
	case "size":
		return compareInt64(a.TotalSizeBytes, b.TotalSizeBytes)
	case "modified":
		return compareTimePtr(a.LatestModifiedAt, b.LatestModifiedAt)
	case "status":
		return compareInt(localMediaMatchStatusRank(a.MatchStatus), localMediaMatchStatusRank(b.MatchStatus))
	default:
		return compareTimePtr(a.LatestUpdatedAt, b.LatestUpdatedAt)
	}
}

func sortLocalMediaItems(items []models.LocalMediaItem, mode, dir string) {
	desc := dir == "desc"
	sort.SliceStable(items, func(i, j int) bool {
		a := items[i]
		b := items[j]
		cmp := 0
		switch mode {
		case "episode":
			cmp = compareInt(a.SeasonNumber, b.SeasonNumber)
			if cmp == 0 {
				cmp = compareInt(a.EpisodeNumber, b.EpisodeNumber)
			}
		case "quality":
			cmp = compareInt(localMediaQualityScore(a), localMediaQualityScore(b))
		}
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(a.RelativePath), strings.ToLower(b.RelativePath))
		}
		if cmp == 0 {
			cmp = strings.Compare(a.ID, b.ID)
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func localMediaResolvedTitleID(item models.LocalMediaItem) string {
	if strings.TrimSpace(item.MatchedTitleID) != "" {
		return strings.TrimSpace(item.MatchedTitleID)
	}
	if item.Metadata != nil && strings.TrimSpace(item.Metadata.ID) != "" {
		return strings.TrimSpace(item.Metadata.ID)
	}
	return ""
}

func localMediaResolvedTitle(item models.LocalMediaItem) string {
	if item.Metadata != nil && strings.TrimSpace(item.Metadata.Name) != "" {
		return strings.TrimSpace(item.Metadata.Name)
	}
	if strings.TrimSpace(item.MatchedName) != "" {
		return strings.TrimSpace(item.MatchedName)
	}
	if strings.TrimSpace(item.DetectedTitle) != "" {
		return strings.TrimSpace(item.DetectedTitle)
	}
	return strings.TrimSpace(item.FileName)
}

func localMediaResolvedYear(item models.LocalMediaItem) int {
	if item.Metadata != nil && item.Metadata.Year > 0 {
		return item.Metadata.Year
	}
	if item.MatchedYear > 0 {
		return item.MatchedYear
	}
	return item.DetectedYear
}

func normalizeLocalMediaGroupText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(".", " ", "_", " ", "-", " ", "/", " ")
	value = replacer.Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func localMediaMatchStatusRank(status models.LocalMediaMatchStatus) int {
	switch status {
	case models.LocalMediaMatchStatusUnmatched:
		return 4
	case models.LocalMediaMatchStatusLowConfidence:
		return 3
	case models.LocalMediaMatchStatusManual:
		return 2
	case models.LocalMediaMatchStatusMatched:
		return 1
	default:
		return 0
	}
}

func localMediaEpisodeGroupKey(item models.LocalMediaItem) string {
	if item.EpisodeNumber > 0 {
		return fmt.Sprintf("%03d", item.EpisodeNumber)
	}
	if strings.TrimSpace(item.EpisodeTitle) != "" {
		return "title:" + normalizeLocalMediaGroupText(item.EpisodeTitle)
	}
	return "file:" + item.ID
}

func localMediaQualityScore(item models.LocalMediaItem) int {
	score := 0
	if item.Probe != nil {
		score += item.Probe.Width * item.Probe.Height
		if item.Probe.HDRFormat != "" {
			score += 10000000
		}
		if item.Probe.VideoCodec != "" {
			score += len(item.Probe.VideoCodec)
		}
	}
	score += int(item.SizeBytes / (1024 * 1024))
	return score
}

func latestTimePtr(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.After(*current) {
		value := *candidate
		return &value
	}
	return current
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareFloat64(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareTimePtr(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case a.Before(*b):
		return -1
	case a.After(*b):
		return 1
	default:
		return 0
	}
}

func sortLocalMediaEpisodeGroups(groups []models.LocalMediaEpisodeGroup) {
	sort.SliceStable(groups, func(i, j int) bool {
		a := groups[i]
		b := groups[j]
		cmp := compareInt(a.EpisodeNumber, b.EpisodeNumber)
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(a.EpisodeTitle), strings.ToLower(b.EpisodeTitle))
		}
		if cmp == 0 {
			cmp = strings.Compare(a.ID, b.ID)
		}
		return cmp < 0
	})
}

func (s *Service) enrichEpisodeMetadata(ctx context.Context, items []models.LocalMediaItem) {
	if s.metadata == nil || len(items) == 0 {
		return
	}

	type seriesKey struct {
		id string
	}
	type episodeInfo struct {
		title    string
		overview string
		image    *models.Image
	}

	seriesItems := make(map[seriesKey][]*models.LocalMediaItem)
	for i := range items {
		item := &items[i]
		if item.LibraryType != models.LocalMediaLibraryTypeShow || item.SeasonNumber <= 0 || item.EpisodeNumber <= 0 {
			continue
		}
		keyID := localMediaResolvedTitleID(*item)
		if keyID == "" && item.Metadata == nil && strings.TrimSpace(item.MatchedName) == "" && strings.TrimSpace(item.DetectedTitle) == "" {
			continue
		}
		seriesItems[seriesKey{id: fmt.Sprintf("%s|%s|%d", keyID, localMediaResolvedTitle(*item), localMediaResolvedYear(*item))}] = append(seriesItems[seriesKey{id: fmt.Sprintf("%s|%s|%d", keyID, localMediaResolvedTitle(*item), localMediaResolvedYear(*item))}], item)
	}

	for _, groupedItems := range seriesItems {
		if len(groupedItems) == 0 {
			continue
		}
		seed := groupedItems[0]
		query := models.SeriesDetailsQuery{
			TitleID: localMediaResolvedTitleID(*seed),
			Name:    localMediaResolvedTitle(*seed),
			Year:    localMediaResolvedYear(*seed),
		}
		if seed.Metadata != nil {
			query.IMDBID = seed.Metadata.IMDBID
			query.TMDBID = seed.Metadata.TMDBID
			query.TVDBID = seed.Metadata.TVDBID
		}
		details, err := s.metadata.SeriesDetails(ctx, query)
		if err != nil || details == nil {
			continue
		}
		episodeMap := make(map[string]episodeInfo)
		for _, season := range details.Seasons {
			for _, episode := range season.Episodes {
				episodeMap[fmt.Sprintf("%d:%d", season.Number, episode.EpisodeNumber)] = episodeInfo{
					title:    strings.TrimSpace(episode.Name),
					overview: strings.TrimSpace(episode.Overview),
					image:    episode.Image,
				}
			}
		}
		for _, item := range groupedItems {
			if info, ok := episodeMap[fmt.Sprintf("%d:%d", item.SeasonNumber, item.EpisodeNumber)]; ok {
				item.EpisodeTitle = info.title
				item.EpisodeOverview = info.overview
				item.EpisodeImage = info.image
			}
		}
	}
}

func (s *Service) GetItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, ErrItemNotFound
	}

	item, err := s.repo.GetItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, ErrItemNotFound
	}

	library, err := s.repo.GetLibrary(ctx, item.LibraryID)
	if err != nil {
		return nil, err
	}
	if library == nil {
		return nil, ErrLibraryNotFound
	}

	cleanFilePath := filepath.Clean(strings.TrimSpace(item.FilePath))
	if cleanFilePath == "" || cleanFilePath == "." {
		return nil, ErrItemNotFound
	}
	cleanRoot := filepath.Clean(strings.TrimSpace(library.RootPath))
	rel, err := filepath.Rel(cleanRoot, cleanFilePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("local media item path escaped library root")
	}
	if _, err := os.Stat(cleanFilePath); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrItemNotFound
		}
		return nil, err
	}

	copy := *item
	copy.FilePath = cleanFilePath
	hydrateLocalMediaItemExternalIDs(&copy)
	return &copy, nil
}

func (s *Service) DeleteItem(ctx context.Context, itemID string) error {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return ErrItemNotFound
	}

	item, err := s.repo.GetItem(ctx, itemID)
	if err != nil {
		return err
	}
	if item == nil {
		return ErrItemNotFound
	}
	if !item.IsMissing {
		return errors.New("local media item can only be deleted when marked missing")
	}
	return s.repo.DeleteItem(ctx, itemID)
}

func (s *Service) UpdateItemMatch(ctx context.Context, itemID string, input models.LocalMediaMatchInput) (*models.LocalMediaItem, error) {
	item, err := s.repo.GetItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, ErrItemNotFound
	}

	item.MatchedTitleID = strings.TrimSpace(input.MatchedTitleID)
	item.MatchedMediaType = strings.TrimSpace(input.MatchedMediaType)
	item.MatchedName = strings.TrimSpace(input.MatchedName)
	item.MatchedYear = input.MatchedYear
	item.Confidence = input.Confidence
	item.MatchStatus = input.MatchStatus
	if item.MatchStatus == "" {
		item.MatchStatus = models.LocalMediaMatchStatusManual
	}
	item.Metadata = input.Metadata
	item.UpdatedAt = time.Now().UTC()

	if err := s.repo.UpsertItem(ctx, item); err != nil {
		return nil, err
	}
	hydrateLocalMediaItemExternalIDs(item)
	return item, nil
}

func (s *Service) StartScan(ctx context.Context, libraryID string) (models.LocalMediaScanSummary, error) {
	library, err := s.repo.GetLibrary(ctx, libraryID)
	if err != nil {
		return models.LocalMediaScanSummary{}, err
	}
	if library == nil {
		return models.LocalMediaScanSummary{}, ErrLibraryNotFound
	}

	s.mu.Lock()
	if state, ok := s.scans[libraryID]; ok && state.inProgress {
		s.mu.Unlock()
		return models.LocalMediaScanSummary{}, ErrLibraryScanning
	}
	s.scans[libraryID] = scanState{inProgress: true, startedAt: time.Now().UTC()}
	s.mu.Unlock()

	return s.scanLibrary(context.WithoutCancel(ctx), *library)
}

func (s *Service) scanLibrary(ctx context.Context, library models.LocalMediaLibrary) (models.LocalMediaScanSummary, error) {
	defer func() {
		s.mu.Lock()
		delete(s.scans, library.ID)
		s.mu.Unlock()
	}()

	startedAt := time.Now().UTC()
	library.LastScanStartedAt = &startedAt
	library.LastScanFinishedAt = nil
	library.LastScanStatus = models.LocalMediaScanStatusScanning
	library.LastScanError = ""
	library.LastScanDiscovered = 0
	library.LastScanTotal = 0
	library.LastScanMatched = 0
	library.LastScanLowConf = 0
	library.UpdatedAt = startedAt
	log.Printf("[localmedia] scan started library=%q id=%s type=%s root=%q", library.Name, library.ID, library.Type, library.RootPath)
	if err := s.repo.UpdateLibrary(ctx, &library); err != nil {
		log.Printf("[localmedia] failed to mark scan started library=%q id=%s: %v", library.Name, library.ID, err)
	}

	scanID := uuid.NewString()
	summary, err := s.discoverAndMatch(ctx, library, scanID)
	finishedAt := time.Now().UTC()
	library.LastScanFinishedAt = &finishedAt
	library.UpdatedAt = finishedAt
	library.LastScanDiscovered = summary.Discovered
	library.LastScanMatched = summary.Matched
	library.LastScanLowConf = summary.LowConfidence
	if err != nil {
		library.LastScanStatus = models.LocalMediaScanStatusFailed
		library.LastScanError = err.Error()
		if updateErr := s.repo.UpdateLibrary(ctx, &library); updateErr != nil {
			log.Printf("[localmedia] failed to persist failed scan state library=%q id=%s: %v", library.Name, library.ID, updateErr)
		}
		log.Printf("[localmedia] scan failed library=%q id=%s discovered=%d matched=%d low_confidence=%d unmatched=%d err=%v", library.Name, library.ID, summary.Discovered, summary.Matched, summary.LowConfidence, summary.Unmatched, err)
		return summary, err
	}

	if err := s.repo.MarkItemsMissingNotSeenInScan(ctx, library.ID, scanID, finishedAt); err != nil {
		library.LastScanStatus = models.LocalMediaScanStatusFailed
		library.LastScanError = err.Error()
		if updateErr := s.repo.UpdateLibrary(ctx, &library); updateErr != nil {
			log.Printf("[localmedia] failed to persist failed delete-missing state library=%q id=%s: %v", library.Name, library.ID, updateErr)
		}
		log.Printf("[localmedia] scan failed library=%q id=%s during=mark_missing err=%v", library.Name, library.ID, err)
		return summary, err
	}

	library.LastScanStatus = models.LocalMediaScanStatusComplete
	library.LastScanError = ""
	if err := s.repo.UpdateLibrary(ctx, &library); err != nil {
		log.Printf("[localmedia] failed to persist completed scan state library=%q id=%s: %v", library.Name, library.ID, err)
		return summary, err
	}

	log.Printf("[localmedia] scan completed library=%q id=%s discovered=%d matched=%d low_confidence=%d unmatched=%d", library.Name, library.ID, summary.Discovered, summary.Matched, summary.LowConfidence, summary.Unmatched)
	return summary, nil
}

func (s *Service) discoverAndMatch(ctx context.Context, library models.LocalMediaLibrary, scanID string) (models.LocalMediaScanSummary, error) {
	candidates, err := collectVideoFiles(library.RootPath)
	if err != nil {
		return models.LocalMediaScanSummary{}, err
	}

	videoFiles := make([]scanFileCandidate, 0, len(candidates))
	skippedByTerm := 0
	skippedBySize := 0
	for _, candidate := range candidates {
		include, reason := shouldIncludeLocalMediaFile(library, candidate.relativePath, candidate.sizeBytes)
		if include {
			videoFiles = append(videoFiles, candidate)
			continue
		}
		switch reason {
		case "filter_term":
			skippedByTerm++
		case "min_size":
			skippedBySize++
		}
	}

	library.LastScanTotal = len(videoFiles)
	library.UpdatedAt = time.Now().UTC()
	if err := s.repo.UpdateLibrary(ctx, &library); err != nil {
		log.Printf("[localmedia] failed to persist scan total library=%q id=%s: %v", library.Name, library.ID, err)
	}
	log.Printf("[localmedia] discovered %d candidate video files library=%q id=%s included=%d skipped_filter_terms=%d skipped_min_size=%d", len(candidates), library.Name, library.ID, len(videoFiles), skippedByTerm, skippedBySize)

	videoFilePaths := make([]string, 0, len(videoFiles))
	for _, candidate := range videoFiles {
		videoFilePaths = append(videoFilePaths, candidate.path)
	}
	detectedByPath := s.detectTitlesForFiles(videoFilePaths, library.Type)
	summary := models.LocalMediaScanSummary{}
	buildErrors := 0
	reusedItems := 0
	deletedExcludedItems := 0
	metadataCache := &scanMetadataCache{details: make(map[string]*models.Title)}
	existingItems, err := s.repo.ListAllItemsByLibrary(ctx, library.ID)
	if err != nil {
		return models.LocalMediaScanSummary{}, err
	}
	existingByRelativePath := make(map[string]models.LocalMediaItem, len(existingItems))
	for _, item := range existingItems {
		existingByRelativePath[item.RelativePath] = item
	}

	for _, candidate := range candidates {
		include, _ := shouldIncludeLocalMediaFile(library, candidate.relativePath, candidate.sizeBytes)
		if include {
			continue
		}
		existing, exists := existingByRelativePath[candidate.relativePath]
		if !exists {
			continue
		}
		if err := s.repo.DeleteItem(ctx, existing.ID); err != nil {
			return summary, err
		}
		delete(existingByRelativePath, candidate.relativePath)
		deletedExcludedItems++
	}

	for index, candidate := range videoFiles {
		existing, hasExisting := existingByRelativePath[candidate.relativePath]
		item, reused, err := s.buildItem(ctx, library, candidate.path, detectedByPath[candidate.path], metadataCache, scanID, existing, hasExisting)
		if err != nil {
			buildErrors++
			if buildErrors <= 10 || buildErrors%100 == 0 {
				log.Printf("[localmedia] item build failed library=%q id=%s file=%q error_count=%d err=%v", library.Name, library.ID, candidate.path, buildErrors, err)
			}
			continue
		}
		if reused {
			reusedItems++
		}
		summary.Discovered++
		switch item.MatchStatus {
		case models.LocalMediaMatchStatusMatched, models.LocalMediaMatchStatusManual:
			summary.Matched++
		case models.LocalMediaMatchStatusLowConfidence:
			summary.LowConfidence++
		default:
			summary.Unmatched++
		}
		if upsertErr := s.repo.UpsertItem(ctx, &item); upsertErr != nil {
			log.Printf("[localmedia] item upsert failed library=%q id=%s file=%q err=%v", library.Name, library.ID, candidate.path, upsertErr)
			return summary, upsertErr
		}

		processed := index + 1
		if processed <= 5 || processed%100 == 0 || processed == len(videoFiles) {
			library.LastScanDiscovered = processed
			library.LastScanMatched = summary.Matched
			library.LastScanLowConf = summary.LowConfidence
			library.UpdatedAt = time.Now().UTC()
			if err := s.repo.UpdateLibrary(ctx, &library); err != nil {
				log.Printf("[localmedia] failed to persist scan progress library=%q id=%s processed=%d: %v", library.Name, library.ID, processed, err)
			}
			log.Printf(
				"[localmedia] progress library=%q id=%s processed=%d/%d discovered=%d matched=%d low_confidence=%d unmatched=%d build_errors=%d reused=%d current=%q status=%s confidence=%.2f",
				library.Name,
				library.ID,
				processed,
				len(videoFiles),
				summary.Discovered,
				summary.Matched,
				summary.LowConfidence,
				summary.Unmatched,
				buildErrors,
				reusedItems,
				item.RelativePath,
				item.MatchStatus,
				item.Confidence,
			)
		}
	}

	if buildErrors > 0 {
		log.Printf("[localmedia] completed discovery with build errors library=%q id=%s build_errors=%d", library.Name, library.ID, buildErrors)
	}
	if reusedItems > 0 {
		log.Printf("[localmedia] reused %d unchanged items library=%q id=%s", reusedItems, library.Name, library.ID)
	}
	if deletedExcludedItems > 0 {
		log.Printf("[localmedia] removed %d items now excluded by library settings library=%q id=%s", deletedExcludedItems, library.Name, library.ID)
	}
	return summary, nil
}

func validateLocalMediaLibraryInput(input models.LocalMediaLibraryCreateInput) (string, string, []string, int64, error) {
	name := strings.TrimSpace(input.Name)
	rootPath := strings.TrimSpace(input.RootPath)
	if name == "" {
		return "", "", nil, 0, ErrLibraryNameNeeded
	}
	if rootPath == "" {
		return "", "", nil, 0, ErrLibraryPathNeeded
	}
	if input.Type == "" {
		return "", "", nil, 0, ErrLibraryTypeNeeded
	}
	if input.MinFileSizeBytes < 0 {
		return "", "", nil, 0, fmt.Errorf("minimum file size must be zero or greater")
	}

	info, err := os.Stat(rootPath)
	if err != nil {
		return "", "", nil, 0, fmt.Errorf("stat library path: %w", err)
	}
	if !info.IsDir() {
		return "", "", nil, 0, fmt.Errorf("library path is not a directory: %s", rootPath)
	}

	return name, rootPath, normalizeLibraryFilterTerms(input.FilterOutTerms), input.MinFileSizeBytes, nil
}

func normalizeLibraryFilterTerms(terms []string) []string {
	if len(terms) == 0 {
		return []string{}
	}
	normalized := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		trimmed := strings.TrimSpace(term)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return []string{}
	}
	return normalized
}

func (s *Service) buildItem(ctx context.Context, library models.LocalMediaLibrary, filePath string, detected detectedTitle, metadataCache *scanMetadataCache, scanID string, existing models.LocalMediaItem, hasExisting bool) (models.LocalMediaItem, bool, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return models.LocalMediaItem{}, false, err
	}
	relativePath, err := filepath.Rel(library.RootPath, filePath)
	if err != nil {
		return models.LocalMediaItem{}, false, err
	}
	modifiedAt := info.ModTime().UTC()
	now := time.Now().UTC()
	if hasExisting && canReuseLocalMediaItem(existing, filePath, info.Size(), modifiedAt) {
		existing.FilePath = filePath
		existing.FileName = filepath.Base(filePath)
		existing.IsMissing = false
		existing.MissingSince = nil
		existing.LastSeenScanID = scanID
		existing.LastScannedAt = &now
		existing.UpdatedAt = now
		return existing, true, nil
	}

	match := localMediaMatch{
		status: models.LocalMediaMatchStatusUnmatched,
	}
	if library.Type != models.LocalMediaLibraryTypeOther && s.metadata != nil && detected.title != "" {
		match = s.matchMetadata(ctx, library.Type, detected, metadataCache)
	}

	probe := s.probeLocalFile(ctx, filePath, info.Size())
	item := models.LocalMediaItem{
		ID:               uuid.NewString(),
		LibraryID:        library.ID,
		RelativePath:     relativePath,
		FilePath:         filePath,
		FileName:         filepath.Base(filePath),
		LibraryType:      library.Type,
		DetectedTitle:    detected.title,
		DetectedYear:     detected.year,
		SeasonNumber:     detected.season,
		EpisodeNumber:    detected.episode,
		Confidence:       match.confidence,
		MatchStatus:      match.status,
		MatchedTitleID:   match.titleID,
		MatchedMediaType: match.mediaType,
		MatchedName:      match.name,
		MatchedYear:      match.year,
		IsMissing:        false,
		MissingSince:     nil,
		Metadata:         match.metadata,
		Probe:            probe,
		SizeBytes:        info.Size(),
		LastScannedAt:    &now,
		LastSeenScanID:   scanID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if hasExisting {
		item.ID = existing.ID
		item.CreatedAt = existing.CreatedAt
	}
	item.ModifiedAt = &modifiedAt
	return item, false, nil
}

func canReuseLocalMediaItem(existing models.LocalMediaItem, filePath string, size int64, modifiedAt time.Time) bool {
	if existing.FilePath != filePath {
		return false
	}
	if existing.SizeBytes != size {
		return false
	}
	if existing.ModifiedAt == nil {
		return false
	}
	return existing.ModifiedAt.UTC().Equal(modifiedAt.UTC())
}

func hydrateLocalMediaItemResultExternalIDs(result *models.LocalMediaItemListResult) {
	if result == nil {
		return
	}
	for i := range result.Items {
		hydrateLocalMediaItemExternalIDs(&result.Items[i])
	}
}

func hydrateLocalMediaItemExternalIDs(item *models.LocalMediaItem) {
	if item == nil {
		return
	}
	if item.Metadata == nil {
		item.ExternalIDs = nil
		return
	}

	externalIDs := &models.LocalMediaExternalIDs{}
	if v := strings.TrimSpace(item.Metadata.IMDBID); v != "" {
		externalIDs.IMDB = v
	}
	if item.Metadata.TMDBID > 0 {
		externalIDs.TMDB = strconv.FormatInt(item.Metadata.TMDBID, 10)
	}
	if item.Metadata.TVDBID > 0 {
		externalIDs.TVDB = strconv.FormatInt(item.Metadata.TVDBID, 10)
	}
	if externalIDs.IMDB == "" && externalIDs.TMDB == "" && externalIDs.TVDB == "" {
		item.ExternalIDs = nil
		return
	}
	item.ExternalIDs = externalIDs
}

func (s *Service) detectTitlesForFiles(filePaths []string, libraryType models.LocalMediaLibraryType) map[string]detectedTitle {
	results := make(map[string]detectedTitle, len(filePaths))
	if len(filePaths) == 0 {
		return results
	}

	fileNames := make([]string, 0, len(filePaths))
	for _, filePath := range filePaths {
		fileNames = append(fileNames, filepath.Base(filePath))
	}

	parsedByName := make(map[string]*parsett.ParsedTitle, len(fileNames))
	for start := 0; start < len(fileNames); start += parseBatchSize {
		end := start + parseBatchSize
		if end > len(fileNames) {
			end = len(fileNames)
		}
		chunk := fileNames[start:end]
		parsedChunk, err := parsett.ParseTitleBatch(chunk)
		if err != nil {
			log.Printf("[localmedia] parsett batch failed start=%d end=%d err=%v", start, end, err)
			for _, name := range chunk {
				if _, ok := parsedByName[name]; !ok {
					parsedByName[name] = nil
				}
			}
			continue
		}
		for name, parsed := range parsedChunk {
			parsedByName[name] = parsed
		}
	}

	for _, filePath := range filePaths {
		fileName := filepath.Base(filePath)
		parsed := parsedByName[fileName]
		results[filePath] = detectTitle(libraryType, fileName, parsed)
	}

	return results
}

type detectedTitle struct {
	title   string
	year    int
	season  int
	episode int
	imdbID  string
	tmdbID  int64
	tvdbID  int64
}

type localMediaMatch struct {
	status     models.LocalMediaMatchStatus
	confidence float64
	titleID    string
	mediaType  string
	name       string
	year       int
	metadata   *models.Title
}

func (s *Service) matchMetadata(ctx context.Context, libraryType models.LocalMediaLibraryType, detected detectedTitle, metadataCache *scanMetadataCache) localMediaMatch {
	if directMatch := s.matchMetadataByExternalIDs(ctx, libraryType, detected, metadataCache); directMatch.status != "" {
		return directMatch
	}

	mediaType := "movie"
	if libraryType == models.LocalMediaLibraryTypeShow {
		mediaType = "series"
	}

	results, err := s.metadata.Search(ctx, detected.title, mediaType)
	if err != nil || len(results) == 0 {
		return localMediaMatch{status: models.LocalMediaMatchStatusUnmatched}
	}

	best := results[0]
	bestScore := metadataConfidence(detected, best)
	for _, result := range results[1:] {
		if score := metadataConfidence(detected, result); score > bestScore {
			best = result
			bestScore = score
		}
	}

	match := localMediaMatch{
		confidence: roundConfidence(bestScore),
		titleID:    best.Title.ID,
		mediaType:  best.Title.MediaType,
		name:       best.Title.Name,
		year:       best.Title.Year,
	}
	switch {
	case bestScore >= 0.82:
		match.status = models.LocalMediaMatchStatusMatched
	case bestScore >= 0.58:
		match.status = models.LocalMediaMatchStatusLowConfidence
	default:
		match.status = models.LocalMediaMatchStatusUnmatched
	}
	if match.status != models.LocalMediaMatchStatusUnmatched {
		match.metadata = s.fetchMetadataDetails(ctx, best.Title, metadataCache)
	}
	return match
}

func (s *Service) matchMetadataByExternalIDs(ctx context.Context, libraryType models.LocalMediaLibraryType, detected detectedTitle, metadataCache *scanMetadataCache) localMediaMatch {
	if strings.TrimSpace(detected.imdbID) == "" && detected.tmdbID == 0 && detected.tvdbID == 0 {
		return localMediaMatch{}
	}

	var title *models.Title
	switch libraryType {
	case models.LocalMediaLibraryTypeShow:
		details, err := s.metadata.SeriesDetails(ctx, models.SeriesDetailsQuery{
			Name:   detected.title,
			Year:   detected.year,
			IMDBID: detected.imdbID,
			TMDBID: detected.tmdbID,
			TVDBID: detected.tvdbID,
		})
		if err != nil || details == nil {
			return localMediaMatch{}
		}
		title = &details.Title
	default:
		details, err := s.metadata.MovieDetails(ctx, models.MovieDetailsQuery{
			Name:   detected.title,
			Year:   detected.year,
			IMDBID: detected.imdbID,
			TMDBID: detected.tmdbID,
			TVDBID: detected.tvdbID,
		})
		if err != nil || details == nil {
			return localMediaMatch{}
		}
		title = details
	}

	match := localMediaMatch{
		status:     models.LocalMediaMatchStatusMatched,
		confidence: 1,
		titleID:    title.ID,
		mediaType:  title.MediaType,
		name:       title.Name,
		year:       title.Year,
	}
	match.metadata = s.fetchMetadataDetails(ctx, *title, metadataCache)
	return match
}

func (s *Service) fetchMetadataDetails(ctx context.Context, title models.Title, metadataCache *scanMetadataCache) *models.Title {
	cacheKey := strings.TrimSpace(title.MediaType) + ":" + strings.TrimSpace(title.ID)
	if metadataCache != nil && cacheKey != ":" {
		metadataCache.mu.Lock()
		cached := metadataCache.details[cacheKey]
		metadataCache.mu.Unlock()
		if cached != nil {
			copy := *cached
			return &copy
		}
	}

	var resolved *models.Title
	switch title.MediaType {
	case "movie":
		details, err := s.metadata.MovieDetails(ctx, models.MovieDetailsQuery{
			TitleID: title.ID,
			Name:    title.Name,
			Year:    title.Year,
			IMDBID:  title.IMDBID,
			TMDBID:  title.TMDBID,
			TVDBID:  title.TVDBID,
		})
		if err == nil && details != nil {
			resolved = details
		}
	case "series":
		details, err := s.metadata.SeriesDetails(ctx, models.SeriesDetailsQuery{
			TitleID: title.ID,
			Name:    title.Name,
			Year:    title.Year,
			IMDBID:  title.IMDBID,
			TMDBID:  title.TMDBID,
			TVDBID:  title.TVDBID,
		})
		if err == nil && details != nil {
			resolved = &details.Title
		}
	}
	if resolved == nil {
		copy := title
		resolved = &copy
	}
	if metadataCache != nil && cacheKey != ":" && resolved != nil {
		copy := *resolved
		metadataCache.mu.Lock()
		metadataCache.details[cacheKey] = &copy
		metadataCache.mu.Unlock()
	}
	copy := *resolved
	return &copy
}

type ffprobeResponse struct {
	Streams []struct {
		CodecType      string            `json:"codec_type"`
		CodecName      string            `json:"codec_name"`
		Width          int               `json:"width"`
		Height         int               `json:"height"`
		ColorTransfer  string            `json:"color_transfer"`
		ColorPrimaries string            `json:"color_primaries"`
		SideDataList   []map[string]any  `json:"side_data_list"`
		Tags           map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
	} `json:"format"`
}

func (s *Service) probeLocalFile(ctx context.Context, path string, fileSize int64) *models.LocalMediaProbe {
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"-i", path,
	}
	cmd := exec.CommandContext(timeoutCtx, s.ffprobePath, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &models.LocalMediaProbe{SizeBytes: fileSize}
	}

	var resp ffprobeResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return &models.LocalMediaProbe{SizeBytes: fileSize}
	}

	probe := &models.LocalMediaProbe{
		FormatName: resp.Format.FormatName,
		SizeBytes:  fileSize,
	}
	if v, err := strconv.ParseFloat(resp.Format.Duration, 64); err == nil {
		probe.DurationSeconds = v
	}
	if v, err := strconv.ParseInt(resp.Format.Size, 10, 64); err == nil && v > 0 {
		probe.SizeBytes = v
	}
	audioSet := make(map[string]struct{})
	subSet := make(map[string]struct{})
	for _, stream := range resp.Streams {
		switch strings.ToLower(strings.TrimSpace(stream.CodecType)) {
		case "video":
			if probe.VideoCodec == "" {
				probe.VideoCodec = stream.CodecName
				probe.Width = stream.Width
				probe.Height = stream.Height
				probe.HDRFormat = detectHDRFormat(stream.ColorTransfer, stream.ColorPrimaries, stream.SideDataList)
			}
		case "audio":
			probe.AudioStreams++
			if codec := strings.TrimSpace(stream.CodecName); codec != "" {
				audioSet[codec] = struct{}{}
			}
		case "subtitle":
			probe.SubtitleStreams++
			if codec := strings.TrimSpace(stream.CodecName); codec != "" {
				subSet[codec] = struct{}{}
			}
		}
	}
	for codec := range audioSet {
		probe.AudioCodecs = append(probe.AudioCodecs, codec)
	}
	for codec := range subSet {
		probe.SubtitleCodecs = append(probe.SubtitleCodecs, codec)
	}
	sort.Strings(probe.AudioCodecs)
	sort.Strings(probe.SubtitleCodecs)
	return probe
}

func detectHDRFormat(colorTransfer, colorPrimaries string, sideData []map[string]any) string {
	for _, item := range sideData {
		if profile, ok := item["dv_profile"]; ok && profile != nil {
			return "dolby_vision"
		}
	}
	if strings.EqualFold(strings.TrimSpace(colorTransfer), "smpte2084") && strings.EqualFold(strings.TrimSpace(colorPrimaries), "bt2020") {
		return "hdr10"
	}
	return ""
}

var (
	seasonEpisodePattern = regexp.MustCompile(`(?i)[ ._-]s(\d{1,2})e(\d{1,3})`)
	xPattern             = regexp.MustCompile(`(?i)[ ._-](\d{1,2})x(\d{1,3})`)
	yearPattern          = regexp.MustCompile(`(?:19|20)\d{2}`)
	cleanupTokens        = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|bluray|bdrip|brrip|webrip|web-dl|webdl|remux|x264|x265|h264|h265|hevc|dv|hdr|hdr10|aac|ac3|dts|truehd|atmos|yts|proper|repack|extended|uncut)\b`)
	bracketPattern       = regexp.MustCompile(`[\[\(\{].*?[\]\)\}]`)
	spacePattern         = regexp.MustCompile(`[._]+`)
)

func detectTitle(libraryType models.LocalMediaLibraryType, fileName string, parsed *parsett.ParsedTitle) detectedTitle {
	if parsed != nil {
		result := detectedTitle{
			title:  strings.TrimSpace(parsed.Title),
			year:   parsed.Year,
			imdbID: strings.TrimSpace(parsed.IMDBID),
			tmdbID: parsed.TMDBID,
			tvdbID: parsed.TVDBID,
		}
		if len(parsed.Seasons) > 0 {
			result.season = parsed.Seasons[0]
		}
		if len(parsed.Episodes) > 0 {
			result.episode = parsed.Episodes[0]
		}
		if result.title != "" {
			return result
		}
	}

	name := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	name = bracketPattern.ReplaceAllString(name, " ")
	name = spacePattern.ReplaceAllString(name, " ")
	name = cleanupTokens.ReplaceAllString(name, " ")

	result := detectedTitle{}
	if loc := seasonEpisodePattern.FindStringIndex(name); loc != nil {
		matches := seasonEpisodePattern.FindStringSubmatch(name)
		result.season, _ = strconv.Atoi(matches[1])
		result.episode, _ = strconv.Atoi(matches[2])
		name = name[:loc[0]]
	} else if loc := xPattern.FindStringIndex(name); loc != nil {
		matches := xPattern.FindStringSubmatch(name)
		result.season, _ = strconv.Atoi(matches[1])
		result.episode, _ = strconv.Atoi(matches[2])
		name = name[:loc[0]]
	}

	if libraryType != models.LocalMediaLibraryTypeOther {
		years := yearPattern.FindAllString(name, -1)
		if len(years) > 0 {
			result.year, _ = strconv.Atoi(years[len(years)-1])
			name = strings.Replace(name, years[len(years)-1], " ", 1)
		}
	}

	name = strings.Join(strings.Fields(name), " ")
	result.title = strings.TrimSpace(name)
	result.imdbID, result.tmdbID, result.tvdbID = extractExternalIDs(fileName)
	return result
}

var (
	imdbIDPattern = regexp.MustCompile(`(?i)\b(tt\d{7,10})\b`)
	tmdbIDPattern = regexp.MustCompile(`(?i)\btmdb[-_. ]?(\d{1,10})\b`)
	tvdbIDPattern = regexp.MustCompile(`(?i)\btvdb[-_. ]?(\d{1,10})\b`)
)

func extractExternalIDs(value string) (string, int64, int64) {
	var imdbID string
	var tmdbID int64
	var tvdbID int64

	if matches := imdbIDPattern.FindStringSubmatch(value); len(matches) > 1 {
		imdbID = strings.ToLower(strings.TrimSpace(matches[1]))
	}
	if matches := tmdbIDPattern.FindStringSubmatch(value); len(matches) > 1 {
		tmdbID, _ = strconv.ParseInt(matches[1], 10, 64)
	}
	if matches := tvdbIDPattern.FindStringSubmatch(value); len(matches) > 1 {
		tvdbID, _ = strconv.ParseInt(matches[1], 10, 64)
	}

	return imdbID, tmdbID, tvdbID
}

func collectVideoFiles(root string) ([]scanFileCandidate, error) {
	var files []scanFileCandidate
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if isVideoFile(path) {
			info, infoErr := d.Info()
			if infoErr != nil {
				return nil
			}
			relativePath, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			files = append(files, scanFileCandidate{
				path:         path,
				relativePath: relativePath,
				sizeBytes:    info.Size(),
			})
		}
		return nil
	})
	return files, err
}

func shouldIncludeLocalMediaFile(library models.LocalMediaLibrary, relativePath string, sizeBytes int64) (bool, string) {
	if library.MinFileSizeBytes > 0 && sizeBytes < library.MinFileSizeBytes {
		return false, "min_size"
	}

	if len(library.FilterOutTerms) == 0 {
		return true, ""
	}

	normalizedPath := strings.ToLower(strings.ReplaceAll(relativePath, "\\", "/"))
	for _, rawTerm := range library.FilterOutTerms {
		term := strings.ToLower(strings.TrimSpace(rawTerm))
		if term == "" {
			continue
		}
		if strings.Contains(normalizedPath, term) {
			return false, "filter_term"
		}
	}
	return true, ""
}

func isVideoFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".ts", ".m2ts", ".wmv", ".mpg", ".mpeg", ".webm":
		return true
	default:
		return false
	}
}

func metadataConfidence(detected detectedTitle, result models.SearchResult) float64 {
	nameScore := similarityScore(detected.title, result.Title.Name)
	yearScore := 0.5
	if detected.year == 0 || result.Title.Year == 0 {
		yearScore = 0.55
	} else if detected.year == result.Title.Year {
		yearScore = 1
	} else if absInt(detected.year-result.Title.Year) == 1 {
		yearScore = 0.7
	} else {
		yearScore = 0.2
	}
	searchScore := math.Min(float64(result.Score)/100.0, 1.0)
	if result.Score == 0 {
		searchScore = 0.5
	}
	return (nameScore * 0.65) + (yearScore * 0.2) + (searchScore * 0.15)
}

func similarityScore(a, b string) float64 {
	na := normalizeName(a)
	nb := normalizeName(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 1
	}

	tokensA := strings.Fields(na)
	tokensB := strings.Fields(nb)
	if len(tokensA) == 0 || len(tokensB) == 0 {
		return 0
	}

	set := make(map[string]struct{}, len(tokensA))
	for _, token := range tokensA {
		set[token] = struct{}{}
	}
	matches := 0
	for _, token := range tokensB {
		if _, ok := set[token]; ok {
			matches++
		}
	}

	tokenScore := float64(matches) / float64(maxInt(len(tokensA), len(tokensB)))
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		tokenScore = math.Max(tokenScore, 0.85)
	}
	return tokenScore
}

func normalizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = regexp.MustCompile(`[^a-z0-9 ]+`).ReplaceAllString(value, " ")
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func roundConfidence(v float64) float64 {
	return math.Round(v*100) / 100
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
