package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novastream/models"
)

func TestWebAppHandlerServesIndex(t *testing.T) {
	root := writeWebAppFixture(t)
	handler := NewWebAppHandler(root, "/watch")

	for _, target := range []string{"/watch", "/watch/", "/watch/details?id=tt123", "/watch/live"} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if !strings.Contains(rec.Body.String(), "strmr web") {
				t.Fatalf("expected index body, got %q", rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestWebAppHandlerServesAssets(t *testing.T) {
	root := writeWebAppFixture(t)
	handler := NewWebAppHandler(root, "/watch")
	req := httptest.NewRequest(http.MethodGet, "/watch/assets/app.js", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "console.log('app');" {
		t.Fatalf("body = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable asset cache", got)
	}
}

func TestWebAppHandlerMissingAssetReturnsNotFound(t *testing.T) {
	root := writeWebAppFixture(t)
	handler := NewWebAppHandler(root, "/watch")
	req := httptest.NewRequest(http.MethodGet, "/watch/assets/missing.js", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if strings.Contains(rec.Body.String(), "strmr web") {
		t.Fatal("missing asset should not fall back to index.html")
	}
}

func TestWebAppHandlerMissingBundleIsClear(t *testing.T) {
	handler := NewWebAppHandler(filepath.Join(t.TempDir(), "missing"), "/watch")
	req := httptest.NewRequest(http.MethodGet, "/watch", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "web app bundle not found") {
		t.Fatalf("expected clear missing bundle response, got %q", rec.Body.String())
	}
}

func TestWebAppHandlerRejectsTraversal(t *testing.T) {
	root := writeWebAppFixture(t)
	handler := NewWebAppHandler(root, "/watch")
	req := httptest.NewRequest(http.MethodGet, "/watch/../secret.txt", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

type fakeWebPlaybackUsers struct {
	users []models.User
}

func (f fakeWebPlaybackUsers) List() []models.User {
	return f.users
}

// fakeWebPlaybackSessions validates a single known token to the configured session.
type fakeWebPlaybackSessions struct {
	token   string
	session models.Session
}

func (f fakeWebPlaybackSessions) Validate(token string) (models.Session, error) {
	if f.token != "" && token == f.token {
		return f.session, nil
	}
	return models.Session{}, errors.New("invalid session")
}

func TestWebPlaybackHandlerServesStandalonePlayer(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{
		users: []models.User{{ID: "profile-1", Name: "Main"}},
	}, fakeWebPlaybackSessions{token: "tok", session: models.Session{AccountID: "acc", IsMaster: true}}, "/mediastorm")

	req := httptest.NewRequest(http.MethodGet, "/watch?title=Movie&token=tok", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, want := range []string{"<title>mediastorm player</title>", "Starting HLS session", `"/mediastorm"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q", want)
		}
	}
	for _, want := range []string{
		"WEB_PLAYER_STALE_PAUSE_MS",
		"recoverWebPlayerHlsSession('hls-network-error'",
		"recover hls session via seek",
		"let position = progressPositionForSend(options);",
		"Authorization: `Bearer ${AUTH_TOKEN}`",
		"apiUrl(path, { appendToken: false })",
		"apiUrl(endpoint, { appendToken: false })",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected playback template to contain HLS recovery hook %q", want)
		}
	}
	if strings.Contains(body, "let position = isEnded ? duration") {
		t.Fatal("playback template must not turn a premature media ended event into 100% progress")
	}
	if strings.Contains(body, `id="profileSelect"`) || strings.Contains(body, `class="profile-select"`) {
		t.Fatalf("playback page should not render a profile selector")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestAdminPlaybackTemplateDoesNotForceEndedProgressToDuration(t *testing.T) {
	body, err := adminTemplates.ReadFile("admin_templates/playback.html")
	if err != nil {
		t.Fatalf("read admin playback template: %v", err)
	}

	rendered := string(body)
	if strings.Contains(rendered, "let position = isEnded ? duration") {
		t.Fatal("admin playback template must not turn a premature media ended event into 100% progress")
	}
	if !strings.Contains(rendered, "let position = currentWebPlayerAbsoluteTime();") {
		t.Fatal("admin playback template must report the actual playhead on ended events")
	}
}

func TestAdminPlaybackTemplateDoesNotDisplayRankingScore(t *testing.T) {
	body, err := adminTemplates.ReadFile("admin_templates/playback.html")
	if err != nil {
		t.Fatalf("read admin playback template: %v", err)
	}

	rendered := string(body)
	for _, unwanted := range []string{"manual-result-score", "result.totalScore", "Score ${escapeHtml"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("admin playback template still displays ranking score marker %q", unwanted)
		}
	}
}

func TestAdminPlaybackTemplatePreservesAIOStreamsPassthroughFormatting(t *testing.T) {
	body, err := adminTemplates.ReadFile("admin_templates/playback.html")
	if err != nil {
		t.Fatalf("read admin playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"function resolveAIOStreamsPassthroughDisplay(attrs)",
		"manual-result-title-passthrough",
		".manual-result-title-passthrough,",
		"white-space: pre-wrap;",
		"const passthroughDisplay = resolveAIOStreamsPassthroughDisplay(attrs);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("admin playback template missing passthrough formatting hook %q", want)
		}
	}
	if count := strings.Count(rendered, "resolveAIOStreamsPassthroughDisplay(attrs)"); count != 3 {
		t.Fatalf("passthrough resolver occurrence count = %d, want definition plus two renderers", count)
	}
}

func TestWebPlaybackTemplateSendsFinalHeartbeatOnTeardown(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"playbackEnded: Boolean(playbackEnded)",
		"stopHlsSession({ keepalive: true });",
		"ended: Boolean(options.ended),",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing final-heartbeat hook %q", want)
		}
	}
}

func TestWebPlaybackTemplateAutoAdvancesAfterGenuineEpisodeEnd(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"const WEB_ENDED_NEXT_COUNTDOWN_MS = 5000;",
		"webPlayerPlaybackEnded = genuinelyEnded;",
		"if (visible && (webPlayerPromptConfirmed || webPlayerPlaybackEnded)) ensureWatchNextCountdown();",
		"!(creditsAdvance || webPlayerPlaybackEnded)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing end-of-episode auto-advance hook %q", want)
		}
	}
	if strings.Contains(rendered, "!webPlayerPromptConfirmed || !webAutoSkipEnabled('credits') ||") {
		t.Fatal("watch-next countdown must not require a credits marker when playback genuinely ended")
	}
}

func TestWebPlaybackTemplateShowsStreamDetailsOnlyForLivePlayback(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"${isLive ? `<div class=\"seek-spacer\"><span id=\"liveStreamInfo\" class=\"live-stream-info\"></span></div>",
		"if (playbackMeta.isLive) startLiveStreamInfoTracking(video);",
		"video.videoWidth",
		"video.videoHeight",
		"requestVideoFrameCallback",
		"${frameRate} FPS",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing live stream details hook %q", want)
		}
	}
}

func TestWebPlaybackTemplateReturnsLivePlaybackToChannelSelector(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"if (playbackMeta.isLive)",
		"referrer.pathname.endsWith('/watch/live')",
		"return `${basePath || ''}/watch/live`;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing live channel-selector return hook %q", want)
		}
	}
}

func TestWebPlaybackTemplateQualifiesMigrationCandidatesBeforeHandoff(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"/playback/bad-streams?perPage=100&page=",
		"webMigrationCandidateSessionKey(candidate)",
		"String(candidate?.filterStatus || '').toLowerCase() !== 'filtered'",
		"webMigrationPathIdentity(nextPath) === webMigrationPathIdentity(previous.path)",
		"!Array.isArray(metadata?.videoStreams) || !metadata.videoStreams.length",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing migration qualification %q", want)
		}
	}
	for _, forbidden := range []string{"allowMarkedBad:true", "allowMarkedBad: true"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("web playback template must not bypass bad-stream filtering with %q", forbidden)
		}
	}
}

func TestWebPlaybackTemplateProbatesMigrationBeforeAdoption(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"const WEB_MIGRATION_PROBATION_MS = 20000;",
		"const WEB_MIGRATION_PROBATION_ADVANCE_SECONDS = 10;",
		"startWebMigrationProbation({",
		"updateWebMigrationProbation();",
		"replacement passed probation and was adopted",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing migration probation hook %q", want)
		}
	}
}

func TestWebPlaybackTemplateAdvancesMigrationAfterSustainedStall(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"const WEB_MIGRATION_STALL_MS = 15000;",
		"armWebMigrationStallTimer('sustained-buffering');",
		"migration replacement failed probation",
		"migrateWebStream(reason).then((migrated) =>",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing sustained-stall migration hook %q", want)
		}
	}
}

func TestWebPlaybackTemplateResetsSourceLocalStateDuringMigration(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"webPlayerManualAudioSelection = false;",
		"webPlayerManualSubtitleSelection = false;",
		"webPlayerSelectedAudioTrack = null;",
		"webPlayerSelectedSubtitleTrack = -1;",
		"playbackMeta.dv = Boolean(primaryVideo?.hasDolbyVision);",
		"skipStopProgress:true,",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing source-local migration reset %q", want)
		}
	}
}

func TestWebPlaybackTemplatePreservesRewindWhenRecoveringStaleSession(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"const resumeTarget = clampSeekTarget(currentAbsoluteTime() - rewindSeconds);",
		"target: resumeTarget,",
		"const target = clampSeekTarget(options.target ?? currentAbsoluteTime());",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing stale-session rewind behavior %q", want)
		}
	}
}

func TestWebPlaybackTemplateForwardsClientIdentity(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"const CLIENT_ID = routeParam('clientId') ||",
		"...(CLIENT_ID ? { 'X-Client-ID': CLIENT_ID } : {}),",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing client identity forwarding %q", want)
		}
	}
}

func TestWebPlaybackTemplateSynchronizesExternalWatchPartyGuests(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"startProgressTracking();\n        startWatchPartySync();",
		"video.readyState >= 2",
		"reportWatchPartyReady();",
		"?buffering=${encodeURIComponent(String(Boolean(webPlayerBuffering)))}",
		`id="watchPartyPanel"`,
		`id="watchPartyPanelToggle"`,
		"#videoHost.controls-hidden .watch-party-panel",
		"function toggleWatchPartyPanel()",
		"renderWatchPartyMembers(room);",
		"member?.buffering",
		"watchPartyEstimatedSeekStartupSeconds",
		"setWatchPartyPlaybackRate(video, drift);",
		"seekWebPlayer(clampSeekTarget(target + startupLead));",
		"WEB_WATCH_PARTY_HARD_SYNC_COOLDOWN_MS",
		"video.paused && !webPlayerSeekInProgress && !webPlayerSessionRecovering",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing external watch party sync hook %q", want)
		}
	}
}

func TestWebPlaybackTemplateRecoversStalledSeekWithSelectedSubtitlePipeline(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	start := strings.Index(rendered, "async function seekWebPlayer(target)")
	end := strings.Index(rendered, "function currentAbsoluteTime()")
	if start < 0 || end <= start {
		t.Fatal("could not locate web player seek function")
	}
	seek := rendered[start:end]
	for _, want := range []string{
		"const seekSubtitleTrack = Number(webPlayerSelectedSubtitleTrack);",
		"seekParams.set('subtitleTrack', String(seekSubtitleTrack >= 0 ? seekSubtitleTrack : -1));",
		"video.addEventListener('canplay', finishSeek",
		"releaseSeekState(false);",
		"forceNewSession: true",
	} {
		if !strings.Contains(seek, want) {
			t.Fatalf("web player seek missing recovery hook %q", want)
		}
	}
	if strings.Contains(seek, "seekParams.set('subtitleTrack', '-1');") {
		t.Fatal("web player seek must not unconditionally detach the selected embedded subtitle")
	}
}

func TestWebPlaybackTemplateLocksGuestPlaybackControls(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		".btn:disabled,",
		"seek.disabled = WATCH_PARTY_MODE",
		"if (WATCH_PARTY_MODE || !segment || webPlayerAutoSkipInProgress)",
		"if (WATCH_PARTY_MODE || !nextEpisodeInfo || playingNextEpisode)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing guest control lock %q", want)
		}
	}
}

func TestWebPlaybackTemplateChangesEmbeddedSubtitlesThroughSyncedPipeline(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	start := strings.Index(rendered, "function changeSubtitleTrack(value)")
	end := strings.Index(rendered, "async function installSubtitleTrack(video, options = {})")
	if start < 0 || end <= start {
		t.Fatal("could not locate subtitle track change function")
	}
	changeTrack := rendered[start:end]
	for _, want := range []string{
		"if (parsed === EXTERNAL_SUBTITLE_TRACK)",
		"if (parsed >= 0 && hlsSessionId)",
		"const startOffset = currentAbsoluteTime();",
		"startWebPlayerFromSource({ startOffset });",
		"installSubtitleTrack(video);",
	} {
		if !strings.Contains(changeTrack, want) {
			t.Fatalf("subtitle track change missing %q", want)
		}
	}
	if strings.Index(changeTrack, "if (parsed === EXTERNAL_SUBTITLE_TRACK)") > strings.Index(changeTrack, "if (parsed >= 0 && hlsSessionId)") {
		t.Fatal("external subtitles must remain overlay-only without rebuilding HLS")
	}
	for _, want := range []string{
		"X-Subtitle-Timestamp-Base",
		"webPlayerSubtitleTimestampBase",
		"Number(webPlayerSubtitleTimestampBase || 0)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("subtitle overlay missing timestamp alignment hook %q", want)
		}
	}
}

func TestWebPlaybackTemplateMapsUnsupportedPreselectionToTextSubtitle(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}

	rendered := string(body)
	for _, want := range []string{
		"const allSubtitleStreams = Array.isArray(metadata?.subtitleStreams) ? metadata.subtitleStreams : [];",
		"webPlayerSubtitleStreams = allSubtitleStreams.filter(isWebTextSubtitleStream);",
		"!webPlayerSubtitleStreams.some((stream) => Number(stream?.index) === Number(webPlayerSelectedSubtitleTrack))",
		"webLanguageMatches(stream, requestedSubtitle?.language)",
		"webPlayerSelectedSubtitleTrack = Number(replacementSubtitle?.index ?? -1);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing unsupported subtitle fallback %q", want)
		}
	}
}

func TestWebPlaybackHandlerRedirectsWithoutValidSession(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{
		users: []models.User{{ID: "profile-1", Name: "Main"}},
	}, fakeWebPlaybackSessions{token: "tok", session: models.Session{AccountID: "acc"}}, "/mediastorm")

	req := httptest.NewRequest(http.MethodGet, "/watch/playback.html?profileId=profile-1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/mediastorm/watch" {
		t.Fatalf("Location = %q, want /mediastorm/watch", got)
	}
}

func TestWebPlaybackHandlerScopesProfilesForAccountSession(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{
		users: []models.User{
			{ID: "profile-1", Name: "One", AccountID: "account-a"},
			{ID: "profile-2", Name: "Two", AccountID: "account-b"},
		},
	}, fakeWebPlaybackSessions{token: "tok", session: models.Session{AccountID: "account-a", IsMaster: false}}, "")

	req := httptest.NewRequest(http.MethodGet, "/watch?token=tok", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"profile-1"`) {
		t.Fatalf("expected scoped profile in body")
	}
	if strings.Contains(body, `"id":"profile-2"`) {
		t.Fatalf("unexpected profile from another account in body")
	}
}

func TestWebPlaybackHandlerScopesStreamSessionToRequestedProfile(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{
		users: []models.User{
			{ID: "profile-1", Name: "One", AccountID: "account-a"},
			{ID: "profile-2", Name: "Two", AccountID: "account-a"},
		},
	}, fakeWebPlaybackSessions{token: "tok", session: models.Session{
		AccountID: "account-a", Scope: models.SessionScopeStream, ScopeResource: "/movie.mkv",
	}}, "")

	req := httptest.NewRequest(http.MethodGet, "/watch?token=tok&profileId=profile-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"profile-1"`) {
		t.Fatal("expected requested profile in stream-scoped page")
	}
	if strings.Contains(body, `"id":"profile-2"`) {
		t.Fatal("stream-scoped page exposed another profile from the account")
	}
}

func TestWebPlaybackHandlerInjectsProfileShareFlag(t *testing.T) {
	// The share button is gated client-side on the active profile's allowShareLinks
	// flag, which rides in the injected PROFILES (.Users) JSON. Assert the flag is
	// serialized so the player can read it.
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{
		users: []models.User{{ID: "profile-1", Name: "One", AccountID: "acc", AllowShareLinks: true}},
	}, fakeWebPlaybackSessions{token: "tok", session: models.Session{AccountID: "acc", IsMaster: false}}, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/watch?token=tok", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"allowShareLinks":true`) {
		t.Fatalf("expected profile allowShareLinks flag in served PROFILES JSON")
	}
}

func TestWebPlaybackHandlerRejectsUnsupportedMethods(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{}, fakeWebPlaybackSessions{}, "")
	req := httptest.NewRequest(http.MethodPost, "/watch", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q, want GET, HEAD", got)
	}
}

func writeWebAppFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>strmr web</html>"), 0644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("console.log('app');\n"), 0644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	return root
}
