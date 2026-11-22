package recordings

import (
	"context"
	"testing"
	"time"

	"novastream/models"
)

// scheduleRuleRepo keeps the airings rules schedule.
type scheduleRuleRepo struct {
	fakeRuleRepo
	all     []models.RecordingRule
	created []models.Recording
}

func (r *scheduleRuleRepo) ListRules(context.Context, string, bool, bool) ([]models.RecordingRule, error) {
	return r.all, nil
}

func (r *scheduleRuleRepo) CreateScheduledRecordingIfAbsent(_ context.Context, rec *models.Recording) (bool, error) {
	r.created = append(r.created, *rec)
	return true, nil
}

// airingSchedule is a fixed guide.
type airingSchedule map[string][]models.EPGProgram

func (a airingSchedule) GetScheduleMultiple(ids []string, start, end time.Time) map[string][]models.EPGProgram {
	out := map[string][]models.EPGProgram{}
	for _, id := range ids {
		for _, p := range a[id] {
			if p.Stop.After(start) && p.Start.Before(end) {
				out[id] = append(out[id], p)
			}
		}
	}
	return out
}

type fixedChannels []Channel

func (c fixedChannels) RecordingChannels(context.Context, string) ([]Channel, error) { return c, nil }

// Record, then record series (Jellyfin's two buttons): the rule must not record
// the episode the single recording already covers, nor one another rule took.
func TestRulesSkipAiringsAlreadyRecorded(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	current, next, later := now.Add(-10*time.Minute), now.Add(50*time.Minute), now.Add(26*time.Hour)
	ch := Channel{ID: "rtl2", TvgID: "RTL2.de", Name: "RTL2", SourceURL: "http://tv.example/rtl2"}
	show := func(start time.Time) models.EPGProgram {
		return models.EPGProgram{Title: "Berlin – Tag & Nacht", Start: start, Stop: start.Add(time.Hour)}
	}
	single := models.Recording{ID: "single", UserID: "p1", Status: models.RecordingStatusRunning, ChannelID: "rtl2", TvgID: "RTL2.de",
		Title: "Berlin – Tag & Nacht", StartAt: current.Add(20 * time.Second), EndAt: current.Add(time.Hour)}
	dropped := models.Recording{ID: "dropped", UserID: "p1", Status: models.RecordingStatusCancelled, ChannelID: "rtl2",
		Title: "Berlin – Tag & Nacht", StartAt: later, EndAt: later.Add(time.Hour)}
	rule := func(id string) models.RecordingRule {
		return models.RecordingRule{ID: id, UserID: "p1", MatchType: models.RecordingRuleMatchTitle, Pattern: "Berlin – Tag & Nacht", ChannelID: "rtl2", Enabled: true}
	}
	rules := &scheduleRuleRepo{all: []models.RecordingRule{rule("series"), rule("second-rule")}}
	s := &Service{
		repo:  newFakeRecordingRepo(single, dropped),
		rules: rules,
		epg:   airingSchedule{"RTL2.de": {show(current), show(next), show(later)}},
	}
	s.channels = fixedChannels{ch}
	s.expandEnabledRules()

	got := map[time.Time][]string{}
	for _, rec := range rules.created {
		got[rec.StartAt] = append(got[rec.StartAt], rec.RuleID)
	}
	if len(got[current]) != 0 {
		t.Fatalf("the running episode was scheduled again: %v", got[current])
	}
	// One rule takes each later airing; the cancelled single recording does
	// not keep the rule from recording that one.
	if len(got[next]) != 1 || len(got[later]) != 1 {
		t.Fatalf("later airings scheduled by %v / %v, want one rule each", got[next], got[later])
	}
}
