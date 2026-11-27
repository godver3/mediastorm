package recordings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"novastream/models"
)

// frameTimeout bounds one still extraction.
const frameTimeout = 20 * time.Second

// ErrNoRecordingFile is returned for a recording without a file to read.
var ErrNoRecordingFile = errors.New("recording has no file")

// Frame returns a JPEG still from a recording's file, width pixels wide (the
// height keeps the aspect ratio). Clients without a guide image show it as
// the recording's picture. FFmpeg's default stream selection takes the
// largest video stream, so a recording that kept every rendition of an
// adaptive stream gives its best picture.
func (s *Service) Frame(ctx context.Context, id string, width int) ([]byte, error) {
	if strings.TrimSpace(s.ffmpegPath) == "" {
		return nil, errors.New("ffmpeg is not configured")
	}
	rec, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, ErrRecordingNotFound
	}
	path := strings.TrimSpace(rec.OutputPath)
	if path == "" {
		return nil, ErrNoRecordingFile
	}
	width = min(max(width, 160), 1280)
	ctx, cancel := context.WithTimeout(ctx, frameTimeout)
	defer cancel()
	var out, stderr bytes.Buffer
	cmd := execCommandContext(ctx, s.ffmpegPath,
		"-nostdin", "-v", "error",
		"-ss", strconv.FormatFloat(frameOffset(*rec, s.recordedUpTo(*rec, time.Now())).Seconds(), 'f', 1, 64),
		"-i", path,
		"-frames:v", "1", "-an", "-sn",
		"-vf", fmt.Sprintf("scale=%d:-2", width),
		"-c:v", "mjpeg", "-q:v", "4", "-f", "image2", "pipe:1")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, truncateRecordingError(stderr.String()))
	}
	if out.Len() == 0 {
		return nil, errors.New("ffmpeg wrote no frame")
	}
	return out.Bytes(), nil
}

// recordedUpTo is how far a running recording got: now, or for one catching up from its source's
// window (backfill.go) its content start plus what it holds.
func (s *Service) recordedUpTo(rec models.Recording, now time.Time) time.Time {
	if content, followed := s.RecordedContent(rec.ID); followed && rec.ActualStartAt != nil {
		return rec.ActualStartAt.Add(content)
	}
	return now
}

// frameOffset picks the still's position: a tenth into the recording, past
// the padding before the programme, at most ten minutes in and never past
// the middle of what has been recorded so far.
func frameOffset(rec models.Recording, now time.Time) time.Duration {
	start := rec.StartAt
	if rec.ActualStartAt != nil {
		start = *rec.ActualStartAt
	}
	end := rec.EndAt
	switch {
	case rec.ActualEndAt != nil:
		end = *rec.ActualEndAt
	case rec.Status == models.RecordingStatusRunning || rec.Status == models.RecordingStatusStarting:
		end = now
	}
	length := end.Sub(start)
	if length <= 0 {
		return 0
	}
	at := length / 10
	if pad := time.Duration(rec.PaddingBeforeSeconds) * time.Second; at < pad {
		at = pad
	}
	// (The package's own max takes ints; length > 0 keeps this positive.)
	return min(at, 10*time.Minute, length/2)
}
