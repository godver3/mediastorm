package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/services/playback"
)

type profileWarmRef struct {
	adoptMigrationPrewarmMock
	id string
}

func (p *profileWarmRef) GetWarm(string, string) *playback.WarmRef {
	return &playback.WarmRef{PrequeueID: p.id}
}
func (p *profileWarmRef) GetWarmScoped(string, string, string) *playback.WarmRef {
	return &playback.WarmRef{PrequeueID: p.id}
}

type prequeueAccountMap map[string]string

func (p prequeueAccountMap) BelongsToAccount(profile, account string) bool {
	return p[profile] == account
}
func readyProfileEntry(store *playback.PrequeueStore, profile string) *playback.PrequeueEntry {
	entry, _ := store.CreateScoped("movie:1", "Synthetic", profile, "movie", 2026, nil, "details", "global")
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/fixture/" + profile + ".mkv"
		e.AudioTracks = []playback.AudioTrackInfo{{Index: 1, Language: "eng"}}
	})
	entry, _ = store.Get(entry.ID)
	return entry
}
func profilePrequeueRequest(t *testing.T, h *PrequeueHandler, account string) playback.PrequeueResponse {
	t.Helper()
	body, _ := json.Marshal(playback.PrequeueRequest{TitleID: "movie:1", TitleName: "Synthetic", MediaType: "movie", UserID: "profile-b", Reason: "details", SkipHLS: true})
	r := httptest.NewRequest("POST", "/api/playback/prequeue", bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, account))
	w := httptest.NewRecorder()
	h.Prequeue(w, r)
	if w.Code != 200 {
		t.Fatalf("prequeue status=%d", w.Code)
	}
	var reply playback.PrequeueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}
func assertProfileStatusAccessible(t *testing.T, h *PrequeueHandler, id, account string) {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/playback/prequeue/"+id, nil)
	r = mux.SetURLVars(r, map[string]string{"prequeueID": id})
	r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, account))
	r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, _ *http.Request, profile string) bool {
		if profile != "profile-b" {
			http.Error(w, "locked", 403)
			return false
		}
		return true
	})
	w := httptest.NewRecorder()
	h.GetStatus(w, r)
	if w.Code != 200 {
		t.Fatalf("returned preparation is inaccessible to requesting profile: %d", w.Code)
	}
}
func TestPrequeueForeignWarmReferenceUsesOwnReadyEntry(t *testing.T) {
	for _, sameAccount := range []bool{false, true} {
		t.Run(map[bool]string{false: "other account", true: "same account locked sibling"}[sameAccount], func(t *testing.T) {
			store := playback.NewPrequeueStore(time.Hour)
			foreign := readyProfileEntry(store, "profile-a")
			own := readyProfileEntry(store, "profile-b")
			owners := prequeueAccountMap{"profile-a": "account-a", "profile-b": "account-b"}
			if sameAccount {
				owners["profile-a"] = "account-b"
			}
			h := &PrequeueHandler{store: store, prewarmSvc: &profileWarmRef{id: foreign.ID}, users: owners}
			reply := profilePrequeueRequest(t, h, "account-b")
			if reply.PrequeueID != own.ID || reply.Status != playback.PrequeueStatusReady {
				t.Fatalf("foreign preparation returned: %+v", reply)
			}
			assertProfileStatusAccessible(t, h, reply.PrequeueID, "account-b")
			if len(store.ListAll()) != 2 {
				t.Fatal("another profile's preparation was changed")
			}
		})
	}
}
func TestPrequeueForeignWarmReferenceDoesNotProbeOrDeleteItsSource(t *testing.T) {
	store := playback.NewPrequeueStore(time.Hour)
	foreign := readyProfileEntry(store, "profile-a")
	own := readyProfileEntry(store, "profile-b")
	foreign.StreamPath = "https://example.invalid/foreign-fixture.mkv"
	store.Update(foreign.ID, func(e *playback.PrequeueEntry) { e.StreamPath = foreign.StreamPath })
	probes := 0
	validator := func(_ context.Context, path string) error {
		if path == foreign.StreamPath {
			probes++
			return errors.New("foreign source must not be touched")
		}
		return nil
	}
	store.SetStreamPathValidator(validator)
	h := &PrequeueHandler{store: store, prewarmSvc: &profileWarmRef{id: foreign.ID}, externalURLValidator: validator}
	reply := profilePrequeueRequest(t, h, "account-b")
	if reply.PrequeueID != own.ID || probes != 0 || len(store.ListAll()) != 2 {
		t.Fatalf("foreign source touched: id=%s probes=%d entries=%d", reply.PrequeueID, probes, len(store.ListAll()))
	}
}
func TestPrequeueForeignWarmReferenceKeepsOwnPendingWork(t *testing.T) {
	store := playback.NewPrequeueStore(time.Hour)
	foreign := readyProfileEntry(store, "profile-a")
	own, _ := store.CreateScoped("movie:1", "Synthetic", "profile-b", "movie", 2026, nil, "details", "global")
	h := &PrequeueHandler{store: store, prewarmSvc: &profileWarmRef{id: foreign.ID}}
	reply := profilePrequeueRequest(t, h, "account-b")
	if reply.PrequeueID != own.ID || reply.Status != playback.PrequeueStatusQueued {
		t.Fatal("foreign warm entry displaced own in-progress work")
	}
}
func TestPrequeueForeignWarmReferenceCreatesOwnPreparationWhenAbsent(t *testing.T) {
	store := playback.NewPrequeueStore(time.Hour)
	foreign := readyProfileEntry(store, "profile-a")
	h := &PrequeueHandler{store: store, prewarmSvc: &profileWarmRef{id: foreign.ID}, users: prequeueAccountMap{"profile-a": "account-a", "profile-b": "account-b"}}
	reply := profilePrequeueRequest(t, h, "account-b")
	if reply.PrequeueID == foreign.ID || reply.Status != playback.PrequeueStatusQueued {
		t.Fatal("fresh profile inherited a foreign preparation")
	}
	deadline := time.Now().Add(time.Second)
	for {
		own, ok := store.Get(reply.PrequeueID)
		if !ok || own.UserID != "profile-b" {
			t.Fatal("fresh preparation has the wrong owner")
		}
		if own.Status == playback.PrequeueStatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synthetic no-provider worker did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	assertProfileStatusAccessible(t, h, reply.PrequeueID, "account-b")
	if len(store.ListAll()) != 2 {
		t.Fatal("foreign preparation was replaced")
	}
}
func TestPrequeueOwnedWarmReferenceStillReusesReadyPreparation(t *testing.T) {
	store := playback.NewPrequeueStore(time.Hour)
	own := readyProfileEntry(store, "profile-b")
	h := &PrequeueHandler{store: store, prewarmSvc: &profileWarmRef{id: own.ID}}
	reply := profilePrequeueRequest(t, h, "account-b")
	if reply.PrequeueID != own.ID || reply.Status != playback.PrequeueStatusReady || len(store.ListAll()) != 1 {
		t.Fatal("own warmed preparation was not reused")
	}
}
