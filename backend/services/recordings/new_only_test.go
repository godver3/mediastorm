package recordings

import (
	"context"
	"testing"
	"time"

	"novastream/models"
)

func TestIsNewAiring(t *testing.T) {
	newEp := models.EPGProgram{Title: "Show", New: true}
	rerun := models.EPGProgram{Title: "Show", PreviouslyShown: true}
	plain := models.EPGProgram{Title: "Show"}

	cases := []struct {
		name     string
		programs []models.EPGProgram
		program  models.EPGProgram
		want     bool
	}{
		{"flagged new", []models.EPGProgram{newEp, plain}, newEp, true},
		{"flagged rerun", []models.EPGProgram{rerun, plain}, rerun, false},
		{"unflagged when guide flags new airings", []models.EPGProgram{newEp, plain}, plain, false},
		{"unflagged when guide flags only reruns", []models.EPGProgram{rerun, plain}, plain, true},
		{"unflagged when guide has no flags", []models.EPGProgram{plain}, plain, true},
		{"new and previously shown together", []models.EPGProgram{{New: true, PreviouslyShown: true}}, models.EPGProgram{New: true, PreviouslyShown: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNewAiring(tc.program, guideMarksNewAirings(tc.programs)); got != tc.want {
				t.Fatalf("isNewAiring = %v, want %v", got, tc.want)
			}
		})
	}
}

type fakeRuleRepo struct {
	rules  map[string]models.RecordingRule
	paused int
}

func (r *fakeRuleRepo) GetRule(_ context.Context, id string) (*models.RecordingRule, error) {
	rule, ok := r.rules[id]
	if !ok {
		return nil, nil
	}
	return &rule, nil
}
func (r *fakeRuleRepo) ListRules(_ context.Context, userID string, _, _ bool) ([]models.RecordingRule, error) {
	var out []models.RecordingRule
	for _, rule := range r.rules {
		if rule.UserID == userID {
			out = append(out, rule)
		}
	}
	return out, nil
}
func (r *fakeRuleRepo) CreateRule(_ context.Context, rule *models.RecordingRule) error {
	r.rules[rule.ID] = *rule
	return nil
}
func (r *fakeRuleRepo) UpdateRule(_ context.Context, rule *models.RecordingRule) error {
	r.rules[rule.ID] = *rule
	return nil
}
func (r *fakeRuleRepo) DeleteRule(_ context.Context, id string) error {
	delete(r.rules, id)
	return nil
}
func (r *fakeRuleRepo) CancelPendingRuleRecordings(context.Context, string, time.Time) error {
	return nil
}
func (r *fakeRuleRepo) PausePendingRuleRecordings(context.Context, string, time.Time) error {
	r.paused++
	return nil
}
func (r *fakeRuleRepo) UpdatePendingRuleRecordingBuffers(context.Context, string, int, int) error {
	return nil
}
func (r *fakeRuleRepo) CreateScheduledRecordingIfAbsent(context.Context, *models.Recording) (bool, error) {
	return true, nil
}

func TestCreateRuleWithOtherNewOnlyChoiceSwitchesExistingRule(t *testing.T) {
	repo := &fakeRuleRepo{rules: map[string]models.RecordingRule{}}
	svc := &Service{rules: repo, ruleWake: make(chan struct{}, 1)}
	base := models.RecordingRule{UserID: "p1", MatchType: models.RecordingRuleMatchTitle, Pattern: "The News",
		ChannelID: "c1", TvgID: "news.1"}

	first, err := svc.CreateRule(base)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	newOnly := base
	newOnly.NewOnly = true
	switched, err := svc.CreateRule(newOnly)
	if err != nil {
		t.Fatalf("re-create with newOnly: %v", err)
	}
	if switched.ID != first.ID || !switched.NewOnly || len(repo.rules) != 1 {
		t.Fatalf("want rule %s switched to newOnly, got %+v (%d rules)", first.ID, switched, len(repo.rules))
	}
	if repo.paused != 1 {
		t.Fatalf("pending airings parked %d times, want 1", repo.paused)
	}
	if _, err := svc.CreateRule(newOnly); err == nil {
		t.Fatal("identical rule should still be rejected")
	}
}
