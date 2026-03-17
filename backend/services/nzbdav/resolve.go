package nzbdav

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"novastream/models"

	"github.com/javi11/nzbparser"
)

// MakeResolver returns a function compatible with playback.Service.ExternalUsenetResolver.
// It checks nzbdav history for completed items before submitting new NZBs,
// and uses WebDAV PROPFIND to locate the actual video file for streaming.
func MakeResolver(client *Client) func(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.PlaybackResolution, error) {
	return func(ctx context.Context, candidate models.NZBResult, nzbBytes []byte, fileName string) (*models.PlaybackResolution, error) {
		nzbFilename := deriveFilenameFromCandidate(candidate)
		if nzbFilename == "" {
			nzbFilename = fileName
		}
		releaseName := strings.TrimSuffix(nzbFilename, ".nzb")

		// 1. Check nzbdav history — instant reuse if already completed
		if existing := client.FindCompleted(ctx, releaseName); existing != nil {
			log.Printf("[nzbdav] reusing completed: %q (nzo=%s)", existing.JobName, existing.NzoID)
			return buildResolution(ctx, client, existing.JobName, existing.Category, nzbBytes, nzbFilename)
		}

		// 2. Submit to nzbdav and wait
		log.Printf("[nzbdav] submitting: %q", nzbFilename)
		nzoID, err := client.SubmitNZB(ctx, nzbBytes, nzbFilename)
		if err != nil {
			return nil, fmt.Errorf("nzbdav submit: %w", err)
		}

		jobName, category, err := client.WaitForCompletion(ctx, nzoID, 2*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("nzbdav timed out waiting for %s", nzoID)
			}
			return nil, fmt.Errorf("nzbdav: %w", err)
		}

		return buildResolution(ctx, client, jobName, category, nzbBytes, nzbFilename)
	}
}

func buildResolution(ctx context.Context, client *Client, jobName, category string, nzbBytes []byte, nzbFilename string) (*models.PlaybackResolution, error) {
	viewPath, err := client.FindVideoFile(ctx, category, jobName)
	if err != nil {
		return nil, fmt.Errorf("nzbdav find video: %w", err)
	}

	webdavPath := PathPrefix + strings.TrimPrefix(viewPath, "/")

	log.Printf("[nzbdav] resolved: webdavPath=%q", webdavPath)
	return &models.PlaybackResolution{
		HealthStatus:  "healthy",
		FileSize:      estimateFileSize(nzbBytes),
		SourceNZBPath: nzbFilename,
		WebDAVPath:    webdavPath,
	}, nil
}

func deriveFilenameFromCandidate(candidate models.NZBResult) string {
	title := strings.TrimSpace(candidate.Title)
	if title == "" {
		return ""
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == ' ':
			return '.'
		case r == '.' || r == '-' || r == '_', r == '[', r == ']', r == '(', r == ')':
			return r
		default:
			return -1
		}
	}, title)
	if !strings.HasSuffix(strings.ToLower(safe), ".nzb") {
		safe += ".nzb"
	}
	return safe
}

func estimateFileSize(nzbBytes []byte) int64 {
	parsed, err := nzbparser.Parse(bytes.NewReader(nzbBytes))
	if err != nil || len(parsed.Files) == 0 {
		return 0
	}
	var maxSize int64
	for _, f := range parsed.Files {
		var size int64
		for _, seg := range f.Segments {
			size += int64(seg.Bytes)
		}
		if size > maxSize {
			maxSize = size
		}
	}
	return maxSize
}
