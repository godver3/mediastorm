package tasteonboarding

import (
	"context"
	"errors"
	"novastream/models"
	"testing"
	"time"
)

type memoryRepository struct {
	states map[string]models.TasteOnboardingState
	err    error
}

func (r *memoryRepository) Get(ctx context.Context, p string) (models.TasteOnboardingState, error) {
	if r.err != nil {
		return models.TasteOnboardingState{}, r.err
	}
	state, ok := r.states[p]
	if !ok {
		state = models.TasteOnboardingState{Status: "new", Interests: []models.TasteInterest{}}
	}
	return state, nil
}
func (r *memoryRepository) Save(ctx context.Context, p string, state models.TasteOnboardingState) error {
	if r.err != nil {
		return r.err
	}
	state.Interests = models.MergeTasteInterests(r.states[p].Interests, state.Interests)
	r.states[p] = state
	return nil
}
func (r *memoryRepository) Skip(ctx context.Context, p string, now time.Time) error {
	if r.err != nil {
		return r.err
	}
	state := r.states[p]
	state.Status = "skipped"
	state.UpdatedAt = now
	if state.Interests == nil {
		state.Interests = []models.TasteInterest{}
	}
	r.states[p] = state
	return nil
}
func (r *memoryRepository) Delete(ctx context.Context, p string) error {
	if r.err != nil {
		return r.err
	}
	delete(r.states, p)
	return nil
}
func TestStartingInterestsAreDurableEditableAndIndependentOfSkip(t *testing.T) {
	ctx := context.Background()
	repo := &memoryRepository{states: map[string]models.TasteOnboardingState{}}
	s := New(repo)
	title := models.Title{ID: "tmdb:movie:7", MediaType: "movie", Name: "Synthetic", PersonalRating: "love", Poster: &models.Image{URL: "https://synthetic.invalid/image"}, SourceAvailability: &models.SourceAvailability{State: "available"}}
	if err := s.Save(ctx, "first", "manual", []models.Title{title}); err != nil {
		t.Fatal(err)
	}
	state, err := New(repo).Get(ctx, "first")
	if err != nil || state.Status != "completed" || len(state.Interests) != 1 || state.Interests[0].Title.TMDBID != 7 || state.Interests[0].Title.Poster != nil || state.Interests[0].Title.SourceAvailability != nil || state.Interests[0].Title.PersonalRating != "" {
		t.Fatal("state lost on reconnect or retained unrelated metadata")
	}
	before := state.Interests[0].AddedAt
	if err := s.Skip(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Get(ctx, "first")
	if state.Status != "skipped" || len(state.Interests) != 1 || !state.Interests[0].AddedAt.Equal(before) {
		t.Fatal("skip erased or refreshed existing interests")
	}
	other, _ := s.Get(ctx, "second")
	if other.Status != "new" || len(other.Interests) != 0 {
		t.Fatal("interests crossed profiles")
	}
	if err := s.Save(ctx, "first", "manual", []models.Title{}); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Get(ctx, "first")
	if state.Status != "completed" || len(state.Interests) != 0 {
		t.Fatal("empty edit failed")
	}
	if err := s.Delete(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Get(ctx, "first")
	if state.Status != "new" || len(state.Interests) != 0 {
		t.Fatal("reset retained setup data")
	}
}
func TestStartingInterestsRejectUnsupportedKindsSourcesAndUnboundedSelections(t *testing.T) {
	repo := &memoryRepository{states: map[string]models.TasteOnboardingState{}}
	s := New(repo)
	title := models.Title{ID: "tmdb:movie:7", MediaType: "movie"}
	for _, input := range []struct {
		profile, source string
		titles          []models.Title
	}{{"", "manual", nil}, {"p", "external-sync", nil}, {"p", "manual", []models.Title{{ID: "person:7", MediaType: "person"}}}, {"p", "manual", make([]models.Title, MaxInterests+1)}} {
		if !errors.Is(s.Save(context.Background(), input.profile, input.source, input.titles), ErrInvalidSelection) {
			t.Fatal("unsupported selection accepted")
		}
	}
	repo.err = errors.New("synthetic storage outage")
	if s.Save(context.Background(), "p", "manual", []models.Title{title}) == nil || s.Skip(context.Background(), "p") == nil || s.Delete(context.Background(), "p") == nil {
		t.Fatal("storage error swallowed")
	}
	if len(repo.states) != 0 {
		t.Fatal("invalid writes changed stored state")
	}
}
