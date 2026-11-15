package playback

import (
	"context"
	"novastream/internal/nzbfetch"
	"novastream/models"
)

func (s *Service) fetchGatewayNZB(ctx context.Context, endpoint, downloadURL string, candidate models.NZBResult) ([]byte, string, error) {
	return nzbfetch.Fetch(ctx, endpoint, downloadURL, candidate)
}
