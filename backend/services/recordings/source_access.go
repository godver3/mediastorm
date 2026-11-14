package recordings

import (
	"context"
	"errors"
	"time"

	"novastream/models"
)

// SourceAuthorizer rechecks the current catalog at creation and actual start.
// It also covers pending jobs materialized from recording rules.
type SourceAuthorizer interface {
	AuthorizeRecording(context.Context, models.Recording) (string, error)
}

// CapacityReserver is implemented by the Live TV channel provider. The release
// callback must run only after FFmpeg and its provider connection have stopped.
type CapacityReserver interface {
	ReserveRecording(context.Context, models.Recording) (func(), error)
}

func (s *Service) authorizeSource(recording *models.Recording) error {
	authorizer, ok := s.channels.(SourceAuthorizer)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resolved, err := authorizer.AuthorizeRecording(ctx, *recording)
	if err != nil || resolved == "" {
		return errors.New("recording channel is not available to this profile")
	}
	recording.SourceURL = resolved
	return nil
}
