package scheduler

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"novastream/config"
	"novastream/models"
)

type fakeRemoteMediaSyncer struct {
	fail   string
	synced []string
}

func (f *fakeRemoteMediaSyncer) ListLibraries(context.Context) ([]models.RemoteMediaLibrary, error) {
	return []models.RemoteMediaLibrary{
		{ID: "jf", Name: "TV", Provider: models.MediaSourceJellyfin},
		{ID: "px", Name: "Movies", Provider: models.MediaSourcePlex},
	}, nil
}

func (f *fakeRemoteMediaSyncer) Sync(_ context.Context, id string) (int, error) {
	f.synced = append(f.synced, id)
	if id == f.fail {
		return 0, errors.New("server offline")
	}
	return 10, nil
}

func TestExecuteLibraryScanRemote(t *testing.T) {
	tests := []struct {
		name, library, fail, wantErr string
		want                         []string
		count                        int
	}{
		{name: "all without local libraries", library: config.ScheduledTaskLocalMediaAllLibraries, want: []string{"jf", "px"}, count: 20},
		{name: "single plex library", library: "px", want: []string{"px"}, count: 10},
		{name: "failure does not stop others", library: config.ScheduledTaskLocalMediaAllLibraries, fail: "jf", wantErr: "server offline", want: []string{"jf", "px"}, count: 10},
		{name: "deleted library", library: "gone", wantErr: "no longer exists"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := &fakeRemoteMediaSyncer{fail: tt.fail}
			svc := &Service{localMediaService: &fakeLocalMediaScanner{}, remoteMediaService: remote}
			result, err := svc.executeLocalMediaScan(config.ScheduledTask{Config: map[string]string{"libraryId": tt.library}})
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if !reflect.DeepEqual(remote.synced, tt.want) || result.Count != tt.count {
				t.Fatalf("synced = %v, count = %d", remote.synced, result.Count)
			}
		})
	}
}
