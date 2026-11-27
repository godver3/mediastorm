package localmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"novastream/services/kids"
	"novastream/utils/parsett"
)

var (
	ErrLibraryNotFound       = errors.New("local media library not found")
	ErrLibraryScanning       = errors.New("local media library scan already in progress")
	ErrLibraryNameNeeded     = errors.New("library name is required")
	ErrLibraryPathNeeded     = errors.New("library root path is required")
	ErrLibraryTypeNeeded     = errors.New("library type is required")
	ErrItemNotFound          = errors.New("local media item not found")
	ErrLocalMediaNotPlayable = errors.New("local media file is not playable")
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
	mu            sync.Mutex
	details       map[string]*models.Title
	byExternalIDs map[string]localMediaMatch
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

func (s *Service) GetLibrary(ctx context.Context, libraryID string) (*models.LocalMediaLibrary, error) {
	return s.repo.GetLibrary(ctx, strings.TrimSpace(libraryID))
}

func (s *Service) FindMatches(ctx context.Context, query models.LocalMediaMatchQuery) ([]models.LocalMediaMatchedGroup, error) {
	libraries, err := s.repo.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}

	targetLibraryType := normalizeLookupLibraryType(query.MediaType)
	matches := make([]models.LocalMediaMatchedGroup, 0)

	for i := range libraries {
		library := libraries[i]
		if targetLibraryType != "" && library.Type != targetLibraryType {
			continue
		}

		items, err := s.repo.ListAllItemsByLibrary(ctx, library.ID)
		if err != nil {
			return nil, err
		}

		result := &models.LocalMediaItemListResult{Items: items}
		hydrateLocalMediaItemResultExternalIDs(result)

		matchGroupKeys := make(map[string]struct{})
		filteredItems := make([]models.LocalMediaItem, 0, len(result.Items))
		for _, item := range result.Items {
			if item.IsMissing {
				continue
			}
			groupKey, _ := localMediaGroupKey(item)
			if localMediaItemMatchesLookup(item, query) {
				matchGroupKeys[groupKey] = struct{}{}
			}
			filteredItems = append(filteredItems, item)
		}
		if len(matchGroupKeys) == 0 {
			continue
		}

		groupItems := make([]models.LocalMediaItem, 0, len(filteredItems))
		for _, item := range filteredItems {
			groupKey, _ := localMediaGroupKey(item)
			if _, ok := matchGroupKeys[groupKey]; ok {
				groupItems = append(groupItems, item)
			}
		}

		groups := buildLocalMediaGroups(groupItems)
		enrichedItems := collectItemsFromGroups(groups)
		s.enrichEpisodeMetadata(ctx, enrichedItems)
		groups = buildLocalMediaGroups(enrichedItems)

		for _, group := range groups {
			if _, ok := matchGroupKeys[group.ID]; !ok {
				continue
			}
			matches = append(matches, models.LocalMediaMatchedGroup{
				LibraryID:   library.ID,
				LibraryName: library.Name,
				LibraryType: library.Type,
				Group:       group,
			})
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		a := matches[i]
		b := matches[j]
		if a.LibraryName != b.LibraryName {
			return strings.ToLower(a.LibraryName) < strings.ToLower(b.LibraryName)
		}
		if a.Group.Title != b.Group.Title {
			return strings.ToLower(a.Group.Title) < strings.ToLower(b.Group.Title)
		}
		return a.Group.ID < b.Group.ID
	})

	return matches, nil
}

func (s *Service) CreateLibrary(ctx context.Context, input models.LocalMediaLibraryCreateInput) (*models.LocalMediaLibrary, error) {
	name, rootPaths, filterOutTerms, minFileSizeBytes, err := validateLocalMediaLibraryInput(input)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	library := &models.LocalMediaLibrary{
		ID:               uuid.NewString(),
		Name:             name,
		Type:             input.Type,
		RootPath:         rootPaths[0],
		RootPaths:        rootPaths,
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

	name, rootPaths, filterOutTerms, minFileSizeBytes, err := validateLocalMediaLibraryInput(input)
	if err != nil {
		return nil, err
	}
	// Keep the original root first while it remains configured, preserving the
	// relative paths and item IDs already stored for that root.
	for index, root := range rootPaths {
		if root == library.RootPath {
			rootPaths = append([]string{root}, append(rootPaths[:index], rootPaths[index+1:]...)...)
			break
		}
	}

	library.Name = name
	library.Type = input.Type
	library.RootPath = rootPaths[0]
	library.RootPaths = rootPaths
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
	case "", "created", "updated", "name", "confidence", "year", "size", "modified", "status":
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
	if !query.IncludeMissing && result != nil {
		filtered := result.Items[:0]
		for _, item := range result.Items {
			if item.IsMissing {
				continue
			}
			filtered = append(filtered, item)
		}
		result.Items = filtered
		result.Total = len(filtered)
		result.Offset = min(result.Offset, result.Total)
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
	case "", "created", "updated", "name", "confidence", "year", "size", "modified", "status":
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

	filtered := make([]models.LocalMediaItem, 0, len(result.Items))
	for _, item := range result.Items {
		if !query.IncludeMissing && item.IsMissing {
			continue
		}
		if localMediaItemMatchesQuery(item, query.Filter, query.Query) {
			filtered = append(filtered, item)
		}
	}

	groups := buildLocalMediaGroups(filtered)
	groups = kids.FilterLibraryGroupsByRatings(groups, query.MaxMovieRating, query.MaxTVRating)
	groups = filterLocalMediaGroupsByMediaType(groups, query.MediaType)
	alphabetBuckets := localMediaAlphabetBuckets(groups)
	groups = filterLocalMediaGroupsByAlphabet(groups, query.Alphabet)
	sortLocalMediaGroups(groups, query.Sort, query.Dir)

	total := len(groups)
	start := min(query.Offset, total)
	end := min(start+query.Limit, total)
	var pageGroups []models.LocalMediaItemGroup
	if start < end {
		pageGroups = groups[start:end]
	} else {
		pageGroups = []models.LocalMediaItemGroup{}
	}

	if query.IncludeCards {
		return &models.LocalMediaGroupListResult{
			Groups:          trimLocalMediaGroupsToCards(pageGroups),
			Total:           total,
			Limit:           query.Limit,
			Offset:          query.Offset,
			AlphabetBuckets: alphabetBuckets,
		}, nil
	}

	// Enrich only items in the current page so we don't make TMDB/TVDB calls
	// for the entire library on every request.
	pageItems := collectItemsFromGroups(pageGroups)
	s.enrichEpisodeMetadata(ctx, pageItems)
	pageGroups = buildLocalMediaGroups(pageItems)

	return &models.LocalMediaGroupListResult{
		Groups:          pageGroups,
		Total:           total,
		Limit:           query.Limit,
		Offset:          query.Offset,
		AlphabetBuckets: alphabetBuckets,
	}, nil
}

func filterLocalMediaGroupsByAlphabet(groups []models.LocalMediaItemGroup, alphabet string) []models.LocalMediaItemGroup {
	alphabet = strings.ToUpper(strings.TrimSpace(alphabet))
	if alphabet == "" {
		return groups
	}
	filtered := groups[:0]
	for _, group := range groups {
		if localMediaAlphabetBucket(group.Title) == alphabet {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func localMediaAlphabetBuckets(groups []models.LocalMediaItemGroup) []string {
	set := make(map[string]struct{})
	for _, group := range groups {
		set[localMediaAlphabetBucket(group.Title)] = struct{}{}
	}
	buckets := make([]string, 0, len(set))
	for bucket := range set {
		buckets = append(buckets, bucket)
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i] == "#" {
			return true
		}
		if buckets[j] == "#" {
			return false
		}
		return buckets[i] < buckets[j]
	})
	return buckets
}

func localMediaAlphabetBucket(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	for _, article := range []string{"THE ", "AN ", "A "} {
		if strings.HasPrefix(name, article) {
			name = strings.TrimSpace(name[len(article):])
			break
		}
	}
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return "#"
	}
	return name[:1]
}

func filterLocalMediaGroupsByMediaType(groups []models.LocalMediaItemGroup, mediaType string) []models.LocalMediaItemGroup {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" || mediaType == "all" {
		return groups
	}
	want := models.LocalMediaLibraryTypeMovie
	if mediaType == "series" || mediaType == "tv" || mediaType == "show" {
		want = models.LocalMediaLibraryTypeShow
	}
	filtered := groups[:0]
	for _, group := range groups {
		if group.LibraryType == want {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func trimLocalMediaGroupsToCards(groups []models.LocalMediaItemGroup) []models.LocalMediaItemGroup {
	trimmed := make([]models.LocalMediaItemGroup, len(groups))
	for i, group := range groups {
		group.Items = nil
		group.Seasons = nil
		trimmed[i] = group
	}
	return trimmed
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

func localMediaItemMatchesLookup(item models.LocalMediaItem, query models.LocalMediaMatchQuery) bool {
	if item.MatchStatus == models.LocalMediaMatchStatusUnmatched {
		return false
	}

	queryTitleID := strings.TrimSpace(query.TitleID)
	if queryTitleID != "" && strings.EqualFold(localMediaResolvedTitleID(item), queryTitleID) {
		return true
	}

	queryIMDB := strings.ToLower(strings.TrimSpace(query.IMDBID))
	queryTMDB := strings.TrimSpace(query.TMDBID)
	queryTVDB := strings.TrimSpace(query.TVDBID)
	if item.ExternalIDs != nil {
		if queryIMDB != "" && strings.EqualFold(strings.TrimSpace(item.ExternalIDs.IMDB), queryIMDB) {
			return true
		}
		if queryTMDB != "" && strings.TrimSpace(item.ExternalIDs.TMDB) == queryTMDB {
			return true
		}
		if queryTVDB != "" && strings.TrimSpace(item.ExternalIDs.TVDB) == queryTVDB {
			return true
		}
	}

	if queryTitleID != "" || queryIMDB != "" || queryTMDB != "" || queryTVDB != "" {
		return false
	}

	queryTitle := normalizeLocalMediaGroupText(query.Title)
	if queryTitle == "" {
		return false
	}
	itemTitle := normalizeLocalMediaGroupText(localMediaResolvedTitle(item))
	if itemTitle != queryTitle {
		return false
	}

	if query.Year > 0 {
		itemYear := localMediaResolvedYear(item)
		return itemYear == 0 || itemYear == query.Year
	}

	return true
}

func normalizeLookupLibraryType(mediaType string) models.LocalMediaLibraryType {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		return models.LocalMediaLibraryTypeMovie
	case "series", "show", "tv":
		return models.LocalMediaLibraryTypeShow
	default:
		return ""
	}
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

// collectItemsFromGroups flattens all items out of a slice of built groups.
func collectItemsFromGroups(groups []models.LocalMediaItemGroup) []models.LocalMediaItem {
	var items []models.LocalMediaItem
	for _, group := range groups {
		if group.LibraryType == models.LocalMediaLibraryTypeShow {
			for _, season := range group.Seasons {
				for _, episode := range season.Episodes {
					items = append(items, episode.Items...)
				}
			}
		} else {
			items = append(items, group.Items...)
		}
	}
	return items
}

func localMediaGroupKey(item models.LocalMediaItem) (string, string) {
	switch item.LibraryType {
	case models.LocalMediaLibraryTypeShow:
		if id := localMediaResolvedTitleID(item); id != "" {
			return "series:" + id, "show"
		}
		return fmt.Sprintf("series:%s:%d", normalizeLocalMediaGroupText(localMediaResolvedTitle(item)), localMediaResolvedYear(item)), "show"
	case models.LocalMediaLibraryTypeMovie:
		if id := localMediaResolvedTitleID(item); id != "" {
			return "movie:" + id, "title"
		}
		return fmt.Sprintf("movie:%s:%d", normalizeLocalMediaGroupText(localMediaResolvedTitle(item)), localMediaResolvedYear(item)), "title"
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
	if group.Certification == "" && item.Metadata != nil && strings.TrimSpace(item.Metadata.Certification) != "" {
		group.Certification = strings.TrimSpace(item.Metadata.Certification)
	}
	if group.Year == 0 {
		group.Year = localMediaResolvedYear(item)
	}
	if item.Metadata != nil {
		if group.TMDBID == 0 && item.Metadata.TMDBID > 0 {
			group.TMDBID = item.Metadata.TMDBID
		}
		if group.TVDBID == 0 && item.Metadata.TVDBID > 0 {
			group.TVDBID = item.Metadata.TVDBID
		}
		if group.IMDBID == "" && strings.TrimSpace(item.Metadata.IMDBID) != "" {
			group.IMDBID = strings.TrimSpace(item.Metadata.IMDBID)
		}
	}
	if group.Poster == nil && item.Metadata != nil && item.Metadata.Poster != nil {
		group.Poster = item.Metadata.Poster
	}
	if group.TextPoster == nil && item.Metadata != nil && item.Metadata.TextPoster != nil {
		group.TextPoster = item.Metadata.TextPoster
	}
	if group.Backdrop == nil && item.Metadata != nil && item.Metadata.Backdrop != nil {
		group.Backdrop = item.Metadata.Backdrop
	}
	if group.TextBackdrop == nil && item.Metadata != nil && item.Metadata.TextBackdrop != nil {
		group.TextBackdrop = item.Metadata.TextBackdrop
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
	group.LatestCreatedAt = latestTimePtr(group.LatestCreatedAt, &item.CreatedAt)
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
	case "updated":
		return compareTimePtr(a.LatestUpdatedAt, b.LatestUpdatedAt)
	default: // "created" and unknown values
		return compareTimePtr(a.LatestCreatedAt, b.LatestCreatedAt)
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
	if item.IsMissing {
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
	if !pathWithinLibraryRoots(*library, cleanFilePath) {
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

func (s *Service) ProbeItemForPlayback(ctx context.Context, item *models.LocalMediaItem) (*models.LocalMediaProbe, error) {
	if item == nil {
		return nil, ErrItemNotFound
	}

	filePath := filepath.Clean(strings.TrimSpace(item.FilePath))
	if filePath == "" || filePath == "." {
		return nil, ErrItemNotFound
	}
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrItemNotFound
		}
		return nil, fmt.Errorf("%w: stat failed: %v", ErrLocalMediaNotPlayable, err)
	}
	if info.IsDir() {
		return nil, ErrItemNotFound
	}

	probe, err := s.probeLocalFileWithError(ctx, filePath, info.Size())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLocalMediaNotPlayable, err)
	}
	if probe == nil || strings.TrimSpace(probe.VideoCodec) == "" {
		return nil, fmt.Errorf("%w: no video stream found", ErrLocalMediaNotPlayable)
	}
	return probe, nil
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
	if err := s.repo.DeleteItem(ctx, itemID); err != nil {
		return err
	}
	return s.refreshLibrarySummary(ctx, item.LibraryID)
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
	if err := s.refreshLibrarySummary(ctx, item.LibraryID); err != nil {
		return nil, err
	}
	hydrateLocalMediaItemExternalIDs(item)
	return item, nil
}

func (s *Service) refreshLibrarySummary(ctx context.Context, libraryID string) error {
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return nil
	}

	library, err := s.repo.GetLibrary(ctx, libraryID)
	if err != nil {
		return err
	}
	if library == nil {
		return ErrLibraryNotFound
	}
	if library.LastScanStatus == models.LocalMediaScanStatusScanning {
		return nil
	}

	items, err := s.repo.ListAllItemsByLibrary(ctx, libraryID)
	if err != nil {
		return err
	}

	discovered := len(items)
	matched := 0
	lowConfidence := 0
	for _, item := range items {
		switch item.MatchStatus {
		case models.LocalMediaMatchStatusMatched, models.LocalMediaMatchStatusManual:
			matched++
		case models.LocalMediaMatchStatusLowConfidence:
			lowConfidence++
		}
	}

	library.LastScanDiscovered = discovered
	library.LastScanMatched = matched
	library.LastScanLowConf = lowConfidence
	if library.LastScanTotal < discovered {
		library.LastScanTotal = discovered
	}
	library.UpdatedAt = time.Now().UTC()
	return s.repo.UpdateLibrary(ctx, library)
}

// StartScan accepts a library scan and runs it in the background so long-lived
// HTTP clients (admin UI) are not tied to scan duration. Progress and completion
// are exposed via library lastScan* fields.
func (s *Service) StartScan(ctx context.Context, libraryID string) (models.LocalMediaScanSummary, error) {
	library, err := s.beginScan(ctx, libraryID)
	if err != nil {
		return models.LocalMediaScanSummary{}, err
	}

	// Persist scanning status before returning so polls see the job immediately.
	if err := s.markLibraryScanning(ctx, library); err != nil {
		s.finishScan(library.ID)
		return models.LocalMediaScanSummary{}, err
	}

	libCopy := *library
	go func() {
		if _, err := s.scanLibrary(context.Background(), libCopy); err != nil {
			log.Printf("[localmedia] background scan failed library=%q id=%s: %v", libCopy.Name, libCopy.ID, err)
		}
	}()

	return models.LocalMediaScanSummary{
		Status: string(models.LocalMediaScanStatusScanning),
		Async:  true,
	}, nil
}

// RunScan runs a library scan synchronously and returns the final summary.
// Used by the scheduler and tests that need completion results.
func (s *Service) RunScan(ctx context.Context, libraryID string) (models.LocalMediaScanSummary, error) {
	library, err := s.beginScan(ctx, libraryID)
	if err != nil {
		return models.LocalMediaScanSummary{}, err
	}
	return s.scanLibrary(context.WithoutCancel(ctx), *library)
}

func (s *Service) beginScan(ctx context.Context, libraryID string) (*models.LocalMediaLibrary, error) {
	library, err := s.repo.GetLibrary(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	if library == nil {
		return nil, ErrLibraryNotFound
	}

	s.mu.Lock()
	if state, ok := s.scans[libraryID]; ok && state.inProgress {
		s.mu.Unlock()
		return nil, ErrLibraryScanning
	}
	s.scans[libraryID] = scanState{inProgress: true, startedAt: time.Now().UTC()}
	s.mu.Unlock()
	return library, nil
}

func (s *Service) finishScan(libraryID string) {
	s.mu.Lock()
	delete(s.scans, libraryID)
	s.mu.Unlock()
}

func (s *Service) markLibraryScanning(ctx context.Context, library *models.LocalMediaLibrary) error {
	if library == nil {
		return ErrLibraryNotFound
	}
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
	if err := s.repo.UpdateLibrary(ctx, library); err != nil {
		log.Printf("[localmedia] failed to mark scan started library=%q id=%s: %v", library.Name, library.ID, err)
		return err
	}
	return nil
}

func (s *Service) scanLibrary(ctx context.Context, library models.LocalMediaLibrary) (models.LocalMediaScanSummary, error) {
	defer s.finishScan(library.ID)

	log.Printf("[localmedia] scan started library=%q id=%s type=%s root=%q", library.Name, library.ID, library.Type, library.RootPath)
	// Always re-assert scanning state at worker start (idempotent with StartScan pre-mark).
	if err := s.markLibraryScanning(ctx, &library); err != nil {
		// Continue the scan even if the status write failed; discover/match still needs to run.
		// Counters may lag until the next successful progress update.
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

	if err := s.repo.DeleteItemsNotSeenInScan(ctx, library.ID, scanID); err != nil {
		library.LastScanStatus = models.LocalMediaScanStatusFailed
		library.LastScanError = err.Error()
		if updateErr := s.repo.UpdateLibrary(ctx, &library); updateErr != nil {
			log.Printf("[localmedia] failed to persist failed delete-missing state library=%q id=%s: %v", library.Name, library.ID, updateErr)
		}
		log.Printf("[localmedia] scan failed library=%q id=%s during=delete_missing err=%v", library.Name, library.ID, err)
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
	var candidates []scanFileCandidate
	for index, root := range libraryRoots(library) {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return models.LocalMediaScanSummary{}, fmt.Errorf("library root unavailable: %s", root)
		}
		found, err := collectVideoFiles(root)
		if err != nil {
			return models.LocalMediaScanSummary{}, err
		}
		if index > 0 {
			prefix := fmt.Sprintf("@root-%x", sha256.Sum256([]byte(root)))[:19]
			for i := range found {
				found[i].relativePath = filepath.Join(prefix, found[i].relativePath)
			}
		}
		candidates = append(candidates, found...)
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
		item, reused, err := s.buildItem(ctx, library, candidate.path, candidate.relativePath, detectedByPath[candidate.path], metadataCache, scanID, existing, hasExisting)
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

func validateLocalMediaLibraryInput(input models.LocalMediaLibraryCreateInput) (string, []string, []string, int64, error) {
	name := strings.TrimSpace(input.Name)
	roots := input.RootPaths
	if len(roots) == 0 && strings.TrimSpace(input.RootPath) != "" {
		roots = []string{input.RootPath}
	}
	if name == "" {
		return "", nil, nil, 0, ErrLibraryNameNeeded
	}
	if len(roots) == 0 {
		return "", nil, nil, 0, ErrLibraryPathNeeded
	}
	if input.Type == "" {
		return "", nil, nil, 0, ErrLibraryTypeNeeded
	}
	if input.MinFileSizeBytes < 0 {
		return "", nil, nil, 0, fmt.Errorf("minimum file size must be zero or greater")
	}
	normalized := make([]string, 0, len(roots))
	for _, raw := range roots {
		root := strings.TrimSpace(raw)
		if root == "" {
			return "", nil, nil, 0, ErrLibraryPathNeeded
		}
		root = filepath.Clean(root)
		info, err := os.Stat(root)
		if err != nil {
			return "", nil, nil, 0, fmt.Errorf("stat library path %s: %w", root, err)
		}
		if !info.IsDir() {
			return "", nil, nil, 0, fmt.Errorf("library path is not a directory: %s", root)
		}
		for _, previous := range normalized {
			if pathWithinRoot(previous, root) || pathWithinRoot(root, previous) {
				return "", nil, nil, 0, fmt.Errorf("library roots must not overlap: %s and %s", previous, root)
			}
		}
		normalized = append(normalized, root)
	}
	return name, normalized, normalizeLibraryFilterTerms(input.FilterOutTerms), input.MinFileSizeBytes, nil
}

func libraryRoots(library models.LocalMediaLibrary) []string {
	if len(library.RootPaths) > 0 {
		return library.RootPaths
	}
	return []string{library.RootPath}
}

func pathWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pathWithinLibraryRoots(library models.LocalMediaLibrary, path string) bool {
	for _, root := range libraryRoots(library) {
		if pathWithinRoot(root, path) {
			return true
		}
	}
	return false
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

func (s *Service) buildItem(ctx context.Context, library models.LocalMediaLibrary, filePath, relativePath string, detected detectedTitle, metadataCache *scanMetadataCache, scanID string, existing models.LocalMediaItem, hasExisting bool) (models.LocalMediaItem, bool, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return models.LocalMediaItem{}, false, err
	}
	modifiedAt := info.ModTime().UTC()
	now := time.Now().UTC()
	if hasExisting && canReuseLocalMediaItem(existing, filePath, info.Size(), modifiedAt) {
		s.rematchReusedItemByExternalIDs(ctx, library, detected, metadataCache, &existing)
		// Season and episode follow the current detection on unchanged files too, so a
		// rescan applies an improved detection (such as the season folder).
		if library.Type == models.LocalMediaLibraryTypeShow && detected.episode > 0 {
			existing.SeasonNumber = detected.season
			existing.EpisodeNumber = detected.episode
		}
		if existing.Probe == nil || existing.Probe.Version < localMediaProbeVersion {
			// Probed before fields were added (interlaced): probe again, keep the rest.
			if probe, err := s.probeLocalFileWithError(ctx, filePath, info.Size()); err == nil {
				existing.Probe = probe
			} else {
				// Tried once: like a new file whose probe fails, not again until it changes.
				log.Printf("[localmedia] re-probe failed file=%q: %v", relativePath, err)
				if existing.Probe == nil {
					existing.Probe = &models.LocalMediaProbe{SizeBytes: info.Size()}
				}
				existing.Probe.Version = localMediaProbeVersion
			}
		}
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
	keepManual := hasExisting && existing.MatchStatus == models.LocalMediaMatchStatusManual
	if !keepManual && library.Type != models.LocalMediaLibraryTypeOther && s.metadata != nil && detected.title != "" {
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
		if keepManual {
			// A changed file at the same path keeps the title an admin chose.
			item.Confidence = existing.Confidence
			item.MatchStatus = existing.MatchStatus
			item.MatchedTitleID = existing.MatchedTitleID
			item.MatchedMediaType = existing.MatchedMediaType
			item.MatchedName = existing.MatchedName
			item.MatchedYear = existing.MatchedYear
			item.Metadata = existing.Metadata
		}
	}
	item.ModifiedAt = &modifiedAt
	return item, false, nil
}

// rematchReusedItemByExternalIDs moves an unchanged file to the title named by
// its ID tags when an earlier scan matched it by title search instead (for
// example before folder tags were read). Manual matches are never touched.
func (s *Service) rematchReusedItemByExternalIDs(ctx context.Context, library models.LocalMediaLibrary, detected detectedTitle, metadataCache *scanMetadataCache, item *models.LocalMediaItem) {
	if s.metadata == nil || library.Type == models.LocalMediaLibraryTypeOther || item.MatchStatus == models.LocalMediaMatchStatusManual {
		return
	}
	match := s.matchMetadataByExternalIDs(ctx, library.Type, detected, metadataCache)
	if match.status == "" || match.titleID == "" || match.titleID == item.MatchedTitleID {
		return
	}
	log.Printf("[localmedia] rematched by external ids file=%q from=%q to=%q", item.RelativePath, item.MatchedTitleID, match.titleID)
	item.Confidence = match.confidence
	item.MatchStatus = match.status
	item.MatchedTitleID = match.titleID
	item.MatchedMediaType = match.mediaType
	item.MatchedName = match.name
	item.MatchedYear = match.year
	item.Metadata = match.metadata
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
	// PostgreSQL TIMESTAMPTZ keeps microseconds while ext4/APFS/btrfs report
	// nanoseconds, so compare at the precision the stored value survives at.
	return existing.ModifiedAt.UTC().Truncate(time.Microsecond).Equal(modifiedAt.UTC().Truncate(time.Microsecond))
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
		detected := detectTitle(libraryType, fileName, parsed)
		applyFolderExternalIDs(&detected, filePath)
		applyFolderSeason(&detected, libraryType, filePath)
		results[filePath] = detected
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
	cacheKey := fmt.Sprintf("%s|%s|%d|%d", libraryType, strings.TrimSpace(detected.imdbID), detected.tmdbID, detected.tvdbID)
	if metadataCache != nil {
		metadataCache.mu.Lock()
		cached, ok := metadataCache.byExternalIDs[cacheKey]
		metadataCache.mu.Unlock()
		if ok {
			if cached.metadata != nil {
				copy := *cached.metadata
				cached.metadata = &copy
			}
			return cached
		}
	}
	match := s.resolveMetadataByExternalIDs(ctx, libraryType, detected, metadataCache)
	if metadataCache != nil {
		stored := match
		if stored.metadata != nil {
			copy := *stored.metadata
			stored.metadata = &copy
		}
		metadataCache.mu.Lock()
		if metadataCache.byExternalIDs == nil {
			metadataCache.byExternalIDs = make(map[string]localMediaMatch)
		}
		metadataCache.byExternalIDs[cacheKey] = stored
		metadataCache.mu.Unlock()
	}
	return match
}

func (s *Service) resolveMetadataByExternalIDs(ctx context.Context, libraryType models.LocalMediaLibraryType, detected detectedTitle, metadataCache *scanMetadataCache) localMediaMatch {

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
		FieldOrder     string            `json:"field_order"`
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

// localMediaProbeVersion is the current LocalMediaProbe generation: 1 added Interlaced.
const localMediaProbeVersion = 1

func (s *Service) probeLocalFile(ctx context.Context, path string, fileSize int64) *models.LocalMediaProbe {
	probe, err := s.probeLocalFileWithError(ctx, path, fileSize)
	if err != nil {
		return &models.LocalMediaProbe{SizeBytes: fileSize, Version: localMediaProbeVersion}
	}
	return probe
}

func (s *Service) probeLocalFileWithError(ctx context.Context, path string, fileSize int64) (*models.LocalMediaProbe, error) {
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
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText != "" {
			return nil, fmt.Errorf("ffprobe failed: %w: %s", err, stderrText)
		}
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var resp ffprobeResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	probe := &models.LocalMediaProbe{
		FormatName: resp.Format.FormatName,
		SizeBytes:  fileSize,
		Version:    localMediaProbeVersion,
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
				probe.Interlaced = models.IsInterlacedFieldOrder(stream.FieldOrder)
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
	return probe, nil
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
	seasonEpisodePattern = regexp.MustCompile(`(?i)[ ._-]s(\d{1,2})e(\d{1,3})(?:\D|$)`)
	xPattern             = regexp.MustCompile(`(?i)[ ._-](\d{1,2})x(\d{1,3})(?:\D|$)`)
	yearPattern          = regexp.MustCompile(`(?:19|20)\d{2}`)
	cleanupTokens        = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|bluray|bdrip|brrip|webrip|web-dl|webdl|remux|x264|x265|h264|h265|hevc|dv|hdr|hdr10|aac|ac3|dts|truehd|atmos|yts|proper|repack|extended|uncut)\b`)
	bracketPattern       = regexp.MustCompile(`[\[\(\{].*?[\]\)\}]`)
	spacePattern         = regexp.MustCompile(`[._]+`)
)

const (
	maxDetectedSeasonNumber  = 99
	maxDetectedEpisodeNumber = 9999 // long-running anime are numbered absolutely (One Piece - 1071)
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
			result.season = normalizeDetectedSeason(parsed.Seasons[0])
		}
		if len(parsed.Episodes) > 0 {
			result.episode = normalizeDetectedEpisode(parsed.Episodes[0])
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
		season, _ := strconv.Atoi(matches[1])
		episode, _ := strconv.Atoi(matches[2])
		result.season = normalizeDetectedSeason(season)
		result.episode = normalizeDetectedEpisode(episode)
		name = name[:loc[0]]
	} else if loc := xPattern.FindStringIndex(name); loc != nil {
		matches := xPattern.FindStringSubmatch(name)
		season, _ := strconv.Atoi(matches[1])
		episode, _ := strconv.Atoi(matches[2])
		result.season = normalizeDetectedSeason(season)
		result.episode = normalizeDetectedEpisode(episode)
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

func normalizeDetectedSeason(value int) int {
	if value < 1 || value > maxDetectedSeasonNumber {
		return 0
	}
	return value
}

func normalizeDetectedEpisode(value int) int {
	if value < 1 || value > maxDetectedEpisodeNumber {
		return 0
	}
	return value
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

// folderExternalIDDepth covers "Show {tvdb-1}/Season 1/file" and
// "Movie {tmdb-1}/file" layouts without reading tags from library roots.
const folderExternalIDDepth = 2

// applyFolderExternalIDs fills IDs from Sonarr/Radarr/Plex-style folder tags
// when the file name carries none. File name IDs win and are never mixed with
// folder IDs.
func applyFolderExternalIDs(detected *detectedTitle, filePath string) {
	if detected.imdbID != "" || detected.tmdbID != 0 || detected.tvdbID != 0 {
		return
	}
	dir := filepath.Dir(filePath)
	for depth := 0; depth < folderExternalIDDepth; depth++ {
		name := filepath.Base(dir)
		if name == "" || name == "." || name == string(filepath.Separator) {
			return
		}
		imdbID, tmdbID, tvdbID := extractExternalIDs(name)
		if imdbID != "" || tmdbID != 0 || tvdbID != 0 {
			detected.imdbID, detected.tmdbID, detected.tvdbID = imdbID, tmdbID, tvdbID
			return
		}
		dir = filepath.Dir(dir)
	}
}

// folderSeasonPattern matches a season folder: "Season 01", "Season 1",
// "Staffel 1", "Saison 2", "S01" (an optional suffix such as " (2021)" after
// the number is allowed).
var folderSeasonPattern = regexp.MustCompile(`(?i)^(?:(?:season|staffel|saison|temporada|stagione)[\s._-]*|s)0*(\d{1,2})(?:$|[^\d])`)

// applyFolderSeason takes an episode's season from its folder ("Show/Season
// 01/Show - 05.mkv") when the file name names an episode but no season, as
// with anime releases. A season in the file name always wins; "Specials" and
// "Season 00" stay season 0.
func applyFolderSeason(detected *detectedTitle, libraryType models.LocalMediaLibraryType, filePath string) {
	if libraryType != models.LocalMediaLibraryTypeShow || detected.season != 0 || detected.episode <= 0 {
		return
	}
	name := strings.TrimSpace(filepath.Base(filepath.Dir(filePath)))
	match := folderSeasonPattern.FindStringSubmatch(name)
	if match == nil {
		return
	}
	if season, err := strconv.Atoi(match[1]); err == nil {
		detected.season = normalizeDetectedSeason(season)
	}
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

// LibraryCopy is a file a local library has of a catalog title.
type LibraryCopy struct {
	Item        models.LocalMediaItem
	LibraryID   string
	LibraryName string
}

// FindLibraryCopies lists the files local libraries have of a catalog title,
// of one episode when season and episode are set (missing files left out),
// matched like FindMatches but without its episode metadata: playback asks.
// With external IDs only that title's rows are read; a lookup by name alone
// reads the library.
//
// Only what a file's name or folder spells out is trusted, since the file
// plays without asking. A special (a Specials or Season 0 folder, "OVA",
// "Recap", "NCED", "05.5") never stands in for an episode, and a special asked
// for is only a file named S00Exx. In a season folder of absolutely numbered
// files (Sonarr anime: "Season 02/Show - 013"), the numbers are absolute.
//
// An episode the libraries have under no file of its own season and number is
// looked for once more among the series' files: in a file holding a short run
// of episodes ("S01E07E08", "- 07-08") that starts with its own episode, and,
// when absolute > 0, under its absolute number in a file named by it alone
// ("One Piece - 1071"), unless the library has the asked season itself. When
// files of different folders or names would qualify (a sequel named without
// a season, season folders the scan did not recognise), none is taken.
func (s *Service) FindLibraryCopies(ctx context.Context, query models.LocalMediaMatchQuery, season, episode, absolute int) ([]LibraryCopy, error) {
	show := episode > 0 && normalizeLookupLibraryType(query.MediaType) == models.LocalMediaLibraryTypeShow
	if !show {
		return s.titleFiles(ctx, query, 0, 0)
	}
	exact, err := s.titleFiles(ctx, query, season, episode)
	if err != nil {
		return nil, err
	}
	exact = filterCopies(exact, func(c LibraryCopy, _ []LibraryCopy) bool {
		item := c.Item
		if item.SeasonNumber != season || item.EpisodeNumber != episode {
			return false
		}
		named := seasonEpisodeToken.MatchString(item.FileName)
		if season == 0 {
			return named
		}
		return named || !librarySpecial(item)
	})
	var candidates []LibraryCopy
	loaded := false
	if len(exact) > 0 {
		// A file whose season only its folder gives may be numbered
		// absolutely: that needs the title's other files to tell.
		check := false
		for _, c := range exact {
			check = check || (season > 1 && !namesSeason(c.Item.FileName))
		}
		if !check {
			return exact, nil
		}
		if candidates, err = s.titleFiles(ctx, query, 0, 0); err != nil {
			return nil, err
		}
		loaded = true
		layout := newTitleLayout(candidates)
		exact = filterCopies(exact, func(c LibraryCopy, _ []LibraryCopy) bool {
			return layout.kind(c, seasonStart(season, episode, absolute)) == seasonNumbered
		})
		if len(exact) > 0 {
			return exact, nil
		}
	}
	if !loaded {
		if candidates, err = s.titleFiles(ctx, query, 0, 0); err != nil {
			return nil, err
		}
	}
	layout := newTitleLayout(candidates)
	start := seasonStart(season, episode, absolute)
	kindOf := func(c LibraryCopy) folderKind {
		if c.Item.SeasonNumber == season {
			return layout.kind(c, start)
		}
		return layout.kind(c, 0)
	}
	found := filterCopies(candidates, func(c LibraryCopy, library []LibraryCopy) bool {
		item := c.Item
		named := seasonEpisodeToken.MatchString(item.FileName)
		if (!named && librarySpecial(item)) || season == 0 {
			return false
		}
		kind := kindOf(c)
		if item.SeasonNumber == season && kind == seasonNumbered && fileHoldsEpisode(item, episode) {
			return true
		}
		if absolute <= 0 || namesSeason(item.FileName) {
			return false
		}
		if item.SeasonNumber > 1 && kind != absoluteNumbered {
			return false
		}
		if item.EpisodeNumber != absolute && !fileHoldsEpisode(item, absolute) {
			return false
		}
		if season > 1 {
			for _, other := range library {
				if other.Item.SeasonNumber == season && kindOf(other) != absoluteNumbered {
					return false // the library has the season itself: numbered by season
				}
			}
		}
		return true
	})
	return dropAmbiguous(found), nil
}

// seasonStart is the absolute number of the asked season's first episode (0:
// unknown).
func seasonStart(season, episode, absolute int) int {
	if season <= 0 || episode <= 0 || absolute < episode {
		return 0
	}
	return absolute - episode + 1
}

// filterCopies keeps the copies keep accepts; keep also gets the copies of the
// same library.
func filterCopies(copies []LibraryCopy, keep func(c LibraryCopy, library []LibraryCopy) bool) []LibraryCopy {
	byLibrary := map[string][]LibraryCopy{}
	for _, c := range copies {
		byLibrary[c.LibraryID] = append(byLibrary[c.LibraryID], c)
	}
	var out []LibraryCopy
	for _, c := range copies {
		if keep(c, byLibrary[c.LibraryID]) {
			out = append(out, c)
		}
	}
	return out
}

// dropAmbiguous takes none when the files go by different names ("Show" and
// "Show II", in any of the libraries), and leaves out a library's files when
// they come from different folders there (versions of one file share both).
func dropAmbiguous(copies []LibraryCopy) []LibraryCopy {
	names := map[string]bool{}
	folders := map[string]map[string]bool{}
	for _, c := range copies {
		names[namePrefix(c.Item)] = true
		if folders[c.LibraryID] == nil {
			folders[c.LibraryID] = map[string]bool{}
		}
		folders[c.LibraryID][itemFolder(c.Item)] = true
	}
	if len(names) > 1 {
		return nil
	}
	var out []LibraryCopy
	for _, c := range copies {
		if len(folders[c.LibraryID]) == 1 {
			out = append(out, c)
		}
	}
	return out
}

// folderKind is how a season folder's files are numbered.
type folderKind int

const (
	seasonNumbered   folderKind = iota // by season ("Season 02/Show - 05" is S02E05)
	absoluteNumbered                   // absolutely (Sonarr anime: "Season 02/Show - 013")
	unclearNumbering                   // cannot tell: neither is trusted
)

// titleLayout tells a title's season folders apart by their files. A season
// folder ("Season 02") whose files name no season and start above episode 1
// is numbered absolutely when the library has the previous season and the
// folder starts after its last episode (at the season's first absolute number,
// when the request tells it); with no previous season it is unclear (season 1
// missing or the first episodes deleted look the same). Only regular files
// count: specials, files without an episode or of another season do not.
type titleLayout struct {
	folderMin map[string]int // library|folder → lowest episode
	seasonMax map[string]int // library|season → highest episode
}

func newTitleLayout(copies []LibraryCopy) titleLayout {
	l := titleLayout{folderMin: map[string]int{}, seasonMax: map[string]int{}}
	for _, c := range copies {
		item := c.Item
		if item.EpisodeNumber <= 0 || item.SeasonNumber <= 0 || librarySpecial(item) {
			continue
		}
		season := c.LibraryID + "|" + strconv.Itoa(item.SeasonNumber)
		if item.EpisodeNumber > l.seasonMax[season] {
			l.seasonMax[season] = item.EpisodeNumber
		}
		if namesSeason(item.FileName) || !folderNamesSeason(item, item.SeasonNumber) {
			continue
		}
		folder := c.LibraryID + "|" + itemFolder(item)
		if m, ok := l.folderMin[folder]; !ok || item.EpisodeNumber < m {
			l.folderMin[folder] = item.EpisodeNumber
		}
	}
	return l
}

// kind tells how the file's season folder is numbered; start is the absolute
// number of that season's first episode, when known (0 otherwise).
func (l titleLayout) kind(c LibraryCopy, start int) folderKind {
	item := c.Item
	if item.SeasonNumber <= 1 || namesSeason(item.FileName) || !folderNamesSeason(item, item.SeasonNumber) {
		return seasonNumbered
	}
	low, ok := l.folderMin[c.LibraryID+"|"+itemFolder(item)]
	if !ok || low <= 1 {
		return seasonNumbered
	}
	previous, ok := l.seasonMax[c.LibraryID+"|"+strconv.Itoa(item.SeasonNumber-1)]
	switch {
	case !ok:
		return unclearNumbering
	case low <= previous:
		return seasonNumbered
	case start > 0 && low != start:
		return unclearNumbering
	}
	return absoluteNumbered
}

// namesSeason reports whether a file's name gives its season ("S01E05",
// "Show S2 - 05", "2nd Season", "Season 2").
func namesSeason(fileName string) bool {
	return seasonEpisodeToken.MatchString(fileName) || seasonNameToken.MatchString(fileName)
}

var (
	// seasonEpisodeToken: a name that numbers its episode by season ("S01E05", "1x05").
	seasonEpisodeToken = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s\d{1,3}[ ._-]?e\d{1,4}|\d{1,2}x\d{1,4})(?:[^0-9]|$)`)
	// multiEpisodeToken: a run of episodes spelled out, "S01E07E08", "E07-E08", "07-08".
	multiEpisodeToken = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s(\d{1,3})[ ._-]?)?e?(\d{1,4})((?:(?:-e?|e)\d{1,4})+)(?:[^a-z0-9]|$)`)
	seasonNameToken   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s\d{1,2}|season[ ._-]?\d{1,2}|\d{1,2}(?:st|nd|rd|th)[ ._-]season)(?:[^a-z0-9]|$)`)
	specialNameToken  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:ova|oad|ona|sp\s?\d{1,3}|specials?|extras?|recap|nc ?op|nc ?ed|creditless|preview|trailer)(?:[^a-z0-9]|$)`)
	halfEpisodeToken  = regexp.MustCompile(`(?:^|[\s_\-\[(])\d{1,4}\.5(?:[\s_\-\])]|\.[a-z0-9]{2,4}$|$)`)
	specialsFolder    = regexp.MustCompile(`(?i)^(?:specials?|extras?|(?:season|staffel|saison)[\s._-]*0+|s0+)$`)
	bracketGroups     = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)
	digitRuns         = regexp.MustCompile(`\d+`)
	nonAlnum          = regexp.MustCompile(`[^a-z0-9]+`)
)

// librarySpecial reports whether a file is a special by its folder or name.
func librarySpecial(item models.LocalMediaItem) bool {
	return specialsFolder.MatchString(itemFolder(item)) || specialNameToken.MatchString(item.FileName) ||
		halfEpisodeToken.MatchString(item.FileName)
}

func itemFolder(item models.LocalMediaItem) string {
	return strings.TrimSpace(filepath.Base(filepath.Dir(filepath.FromSlash(item.RelativePath))))
}

// firstEpisodeIndex is where the file's own episode number first appears in
// its name as a number of its own (-1 when it does not).
func firstEpisodeIndex(item models.LocalMediaItem) int {
	for _, run := range digitRuns.FindAllStringIndex(item.FileName, -1) {
		if n, err := strconv.Atoi(item.FileName[run[0]:run[1]]); err == nil && n == item.EpisodeNumber {
			return run[0]
		}
	}
	return -1
}

// namePrefix is a file's name before its episode number, without release
// groups or tags: "[Group] Show II - 05 (1080p)" → "showii".
func namePrefix(item models.LocalMediaItem) string {
	name := item.FileName
	if i := firstEpisodeIndex(item); i >= 0 {
		name = name[:i]
	}
	return nonAlnum.ReplaceAllString(strings.ToLower(bracketGroups.ReplaceAllString(name, " ")), "")
}

// fileHoldsEpisode reports whether a file's name spells out a run of at most
// four episodes that starts with its own episode and includes episode
// ("Show.S01E07E08", "Show - 07-08"). In a name that numbers by season the run
// must carry the season ("S01E07-08"); otherwise it must be where the name
// first gives the file's episode, not a number in the episode's title. A run
// without a season counts only where the season is not itself a guess from
// the name: season 0/1, or one its folder names.
func fileHoldsEpisode(item models.LocalMediaItem, episode int) bool {
	named := seasonEpisodeToken.MatchString(item.FileName)
	first := firstEpisodeIndex(item)
	for _, m := range multiEpisodeToken.FindAllStringSubmatchIndex(item.FileName, -1) {
		group := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return item.FileName[m[2*i]:m[2*i+1]]
		}
		start, err := strconv.Atoi(group(2))
		if err != nil || start != item.EpisodeNumber {
			continue
		}
		rest := digitRuns.FindAllString(group(3), -1)
		last, err := strconv.Atoi(rest[len(rest)-1])
		if err != nil || last <= start || last-start > 3 || episode <= start || episode > last {
			continue
		}
		if group(1) != "" {
			if named, err := strconv.Atoi(group(1)); err != nil || named != item.SeasonNumber {
				continue
			}
		} else if named || m[4] != first {
			continue
		} else if item.SeasonNumber > 1 && !folderNamesSeason(item, item.SeasonNumber) {
			continue
		}
		return true
	}
	return false
}

// folderNamesSeason reports whether the file's folder is that season's ("Season 05").
func folderNamesSeason(item models.LocalMediaItem, season int) bool {
	match := folderSeasonPattern.FindStringSubmatch(itemFolder(item))
	if match == nil {
		return false
	}
	named, err := strconv.Atoi(match[1])
	return err == nil && named == season
}

// titleFiles lists the present files the local libraries have of a title (of
// one episode when episode > 0, checked by the caller): with external IDs only
// those rows are read, a lookup by name alone reads the libraries.
func (s *Service) titleFiles(ctx context.Context, query models.LocalMediaMatchQuery, season, episode int) ([]LibraryCopy, error) {
	libraries, err := s.repo.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	libraryType := normalizeLookupLibraryType(query.MediaType)
	var copies []LibraryCopy
	for _, library := range libraries {
		if libraryType != "" && library.Type != libraryType {
			continue
		}
		var items []models.LocalMediaItem
		if query.TitleID != "" || query.IMDBID != "" || query.TMDBID != "" || query.TVDBID != "" {
			items, err = s.repo.ListItemsForTitle(ctx, library.ID, query.TitleID, query.IMDBID, query.TMDBID, query.TVDBID, season, episode)
		} else {
			items, err = s.repo.ListAllItemsByLibrary(ctx, library.ID)
		}
		if err != nil {
			return nil, err
		}
		for i := range items {
			item := items[i]
			// A low-confidence match (by name alone) is not played by itself.
			if item.IsMissing || item.MatchStatus == models.LocalMediaMatchStatusLowConfidence {
				continue
			}
			hydrateLocalMediaItemExternalIDs(&item)
			if localMediaItemMatchesLookup(item, query) {
				copies = append(copies, LibraryCopy{Item: item, LibraryID: library.ID, LibraryName: library.Name})
			}
		}
	}
	sort.SliceStable(copies, func(i, j int) bool {
		return strings.ToLower(copies[i].LibraryName) < strings.ToLower(copies[j].LibraryName)
	})
	return copies, nil
}
