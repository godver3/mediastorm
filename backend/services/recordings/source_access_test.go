package recordings

import (
	"context"
	"errors"
	"testing"

	"novastream/models"
)

type authorizingChannels struct{ allowed bool }

func (p *authorizingChannels) RecordingChannels(context.Context, string) ([]Channel, error) {
	return nil, nil
}
func (p *authorizingChannels) AuthorizeRecording(context.Context, models.Recording) (string, error) {
	if !p.allowed {
		return "", errors.New("assignment revoked")
	}
	return "https://catalog.example/allowed", nil
}

func TestRecordingSourceAuthorizationRechecksCurrentAssignment(t *testing.T) {
	provider := &authorizingChannels{allowed: true}
	s := NewService(nil, "", t.TempDir())
	s.channels = provider
	job := models.Recording{UserID: "profile", ChannelID: "channel", SourceURL: "https://submitted.example/private"}
	if err := s.authorizeSource(&job); err != nil || job.SourceURL != "https://catalog.example/allowed" {
		t.Fatal("authorized catalog did not replace submitted URL")
	}
	provider.allowed = false
	if err := s.authorizeSource(&job); err == nil {
		t.Fatal("pending recording retained access after assignment revocation")
	}
}
