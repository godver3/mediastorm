// Package tasteonboarding persists the optional starting interests of a profile.
// It never writes playback history, ratings, lists or external services.
package tasteonboarding

import (
	"context"
	"errors"
	"novastream/models"
	"novastream/services/titleratings"
	"time"
)

const MaxInterests = 40

var ErrInvalidSelection = errors.New("invalid starting interests")

type Repository interface {
	Get(context.Context, string) (models.TasteOnboardingState, error)
	Save(context.Context, string, models.TasteOnboardingState) error
	Skip(context.Context, string, time.Time) error
	Delete(context.Context, string) error
}
type Service struct {
	repo Repository
	now  func() time.Time
}

func New(repo Repository) *Service { return &Service{repo: repo, now: time.Now} }
func (s *Service) Get(ctx context.Context, profile string) (models.TasteOnboardingState, error) {
	return s.repo.Get(ctx, profile)
}
func (s *Service) Save(ctx context.Context, profile, source string, titles []models.Title) error {
	if profile == "" || len(titles) > MaxInterests || source != "manual" && source != "history" {
		return ErrInvalidSelection
	}
	now := s.now().UTC()
	interests := []models.TasteInterest{}
	for _, title := range titles {
		title = titleratings.BareTitle(title)
		aliases := titleratings.Aliases(title)
		if title.ID == "" || len(title.ID) > 128 || len(aliases) == 0 {
			return ErrInvalidSelection
		}
		interests = append(interests, models.TasteInterest{Title: title, Aliases: aliases, AddedAt: now})
	}
	interests = models.MergeTasteInterests(nil, interests)
	return s.repo.Save(ctx, profile, models.TasteOnboardingState{Status: "completed", Source: source, Interests: interests, UpdatedAt: now})
}
func (s *Service) Skip(ctx context.Context, profile string) error {
	return s.repo.Skip(ctx, profile, s.now().UTC())
}
func (s *Service) Delete(ctx context.Context, profile string) error {
	return s.repo.Delete(ctx, profile)
}
