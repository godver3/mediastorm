package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"novastream/config"
	"novastream/services/qualityprofiles"
)

type fakePresetSyncer struct {
	force bool
	res   *qualityprofiles.SyncDueResult
	err   error
}

func (f *fakePresetSyncer) SyncDue(_ context.Context, force bool) (*qualityprofiles.SyncDueResult, error) {
	f.force = force
	return f.res, f.err
}

func TestExecuteQualityPresetSync(t *testing.T) {
	svc := NewService(config.NewManager(filepath.Join(t.TempDir(), "settings.json")), nil, nil, nil)
	task := config.ScheduledTask{ID: "t", Type: config.ScheduledTaskTypeQualityPresetSync, Config: map[string]string{}}
	if _, err := svc.executeQualityPresetSync(task); err == nil {
		t.Error("missing service must be reported")
	}
	fake := &fakePresetSyncer{res: &qualityprofiles.SyncDueResult{Checked: 3, Applied: 1, Staged: 1}}
	svc.SetQualityPresetSyncer(fake)
	res, err := svc.executeQualityPresetSync(task)
	if err != nil || res.Count != 1 || !strings.Contains(res.Message, "Checked 3") || fake.force {
		t.Fatalf("result %+v err %v force %v", res, err, fake.force)
	}
	fake.res = &qualityprofiles.SyncDueResult{Checked: 2, Errors: []string{"German HD: offline"}}
	if _, err := svc.executeQualityPresetSync(task); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("per-preset failures must fail the task run: %v", err)
	}
	fake.err = errors.New("boom")
	task.Config["force"] = "true"
	if _, err := svc.executeQualityPresetSync(task); err == nil || !fake.force {
		t.Errorf("force flag / error not propagated: %v %v", err, fake.force)
	}
}
