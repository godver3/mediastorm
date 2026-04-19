package handlers

import (
	"errors"
	"strings"
	"sync"
	"time"

	nzbfilesystem "novastream/internal/nzb/filesystem"
	nzbfilesystemlegacy "novastream/internal/nzbfilesystem"
	"novastream/internal/usenet"
)

const streamFailureConfirmationTTL = 2 * time.Minute

type streamFailureRecord struct {
	Path       string
	Reason     string
	Error      string
	RecordedAt time.Time
}

type streamFailureRegistry struct {
	mu      sync.Mutex
	records map[string]streamFailureRecord
}

var defaultStreamFailureRegistry = &streamFailureRegistry{
	records: make(map[string]streamFailureRecord),
}

func (r *streamFailureRegistry) recordIfMissingArticles(path string, err error) bool {
	if r == nil || err == nil {
		return false
	}
	reason, ok := missingArticleFailureReason(err)
	if !ok {
		return false
	}

	normalized := normalizeStreamFailurePath(path)
	if normalized == "" {
		return false
	}

	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[normalized] = streamFailureRecord{
		Path:       normalized,
		Reason:     reason,
		Error:      err.Error(),
		RecordedAt: now,
	}
	r.pruneLocked(now)
	return true
}

func (r *streamFailureRegistry) confirmedRecent(path string, maxAge time.Duration) (streamFailureRecord, bool) {
	if r == nil {
		return streamFailureRecord{}, false
	}
	normalized := normalizeStreamFailurePath(path)
	if normalized == "" {
		return streamFailureRecord{}, false
	}
	if maxAge <= 0 {
		maxAge = streamFailureConfirmationTTL
	}

	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	record, ok := r.records[normalized]
	if !ok || now.Sub(record.RecordedAt) > maxAge {
		return streamFailureRecord{}, false
	}
	return record, true
}

func (r *streamFailureRegistry) pruneLocked(now time.Time) {
	for key, record := range r.records {
		if now.Sub(record.RecordedAt) > streamFailureConfirmationTTL {
			delete(r.records, key)
		}
	}
}

func missingArticleFailureReason(err error) (string, bool) {
	var articleErr *usenet.ArticleNotFoundError
	if errors.As(err, &articleErr) {
		return "article_not_found", true
	}

	var partialErr *nzbfilesystem.PartialContentError
	if errors.As(err, &partialErr) {
		return "partial_content_missing_articles", true
	}

	var corruptedErr *nzbfilesystem.CorruptedFileError
	if errors.As(err, &corruptedErr) {
		return "corrupted_missing_articles", true
	}

	var legacyPartialErr *nzbfilesystemlegacy.PartialContentError
	if errors.As(err, &legacyPartialErr) {
		return "partial_content_missing_articles", true
	}

	var legacyCorruptedErr *nzbfilesystemlegacy.CorruptedFileError
	if errors.As(err, &legacyCorruptedErr) {
		return "corrupted_missing_articles", true
	}

	if errors.Is(err, nzbfilesystem.ErrFileIsCorrupted) || errors.Is(err, nzbfilesystemlegacy.ErrFileIsCorrupted) {
		return "corrupted_missing_articles", true
	}

	return "", false
}

func normalizeStreamFailurePath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "webdav/")
	return strings.TrimPrefix(p, "/")
}
