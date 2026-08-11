package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/services/peartube"
)

func pearTubeSettingsBody(t *testing.T, stored config.PearTubeSettings) []byte {
	t.Helper()

	payload := config.DefaultSettings()
	payload.TorrentScrapers = slices.DeleteFunc(payload.TorrentScrapers, func(entry config.TorrentScraperConfig) bool {
		return entry.Type == config.TorrentScraperTypePearTube
	})
	if stored.RelayURL != "" || stored.Enabled != nil {
		payload.TorrentScrapers = append(payload.TorrentScrapers, config.TorrentScraperConfig{
			Name:    "PearTube",
			Type:    config.TorrentScraperTypePearTube,
			URL:     stored.RelayURL,
			Enabled: stored.Enabled == nil || *stored.Enabled,
			Config: map[string]string{
				config.PearTubeConfigConsentVersion:         strconv.Itoa(stored.ConsentVersion),
				config.PearTubeConfigMigrationRequired:      strconv.FormatBool(stored.MigrationRequired),
				config.PearTubeConfigContributeWatchedMedia: strconv.FormatBool(stored.ContributeWatchedMedia),
				config.PearTubeConfigContributionBudget:     strconv.Itoa(stored.ContributionBudget),
				config.PearTubeConfigArchiveEnabled:         strconv.FormatBool(stored.ArchiveEnabled),
				config.PearTubeConfigArchiveBudget:          strconv.Itoa(stored.ArchiveBudget),
			},
		})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return body
}

func putPearTubeSettings(t *testing.T, handler *SettingsHandler, stored config.PearTubeSettings) config.Settings {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.PutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(pearTubeSettingsBody(t, stored))))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}

	saved, err := handler.Manager.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	return saved
}

func TestPutSettingsPersistsExplicitPearTubePolicy(t *testing.T) {
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	handler := NewSettingsHandler(manager)

	saved := putPearTubeSettings(t, handler, config.PearTubeSettings{
		RelayURL:               "http://relay.internal:8178",
		Enabled:                new(true),
		ConsentVersion:         config.PearTubeConsentVersion,
		ContributeWatchedMedia: true,
		ContributionBudget:     21,
		ArchiveEnabled:         true,
		ArchiveBudget:          144,
	})
	stored := saved.PearTubeConfig()
	if stored.RelayURL != "http://relay.internal:8178" || stored.Enabled == nil || !*stored.Enabled {
		t.Fatalf("relay settings not persisted: %+v", stored)
	}
	if !stored.ContributeWatchedMedia || stored.ContributionBudget != 21 ||
		!stored.ArchiveEnabled || stored.ArchiveBudget != 144 ||
		stored.MigrationRequired || stored.EffectiveMode() != config.PearTubeModeArchiveEnabled {
		t.Fatalf("explicit policy did not round-trip: %+v", stored)
	}
}

func TestPearTubeBudgetEditsUseStringWireShapeAndRoundTrip(t *testing.T) {
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	handler := NewSettingsHandler(manager)
	body := []byte(`{"torrentScrapers":[{
		"name":"PearTube","type":"peartube","url":"http://relay.internal:8178","enabled":true,
		"config":{
			"consentVersion":"1","migrationRequired":"false",
			"contributeWatchedMedia":"true","contributionBudget":"34",
			"archiveEnabled":"true","archiveBudget":"233"
		}
	}]}`)

	rec := httptest.NewRecorder()
	handler.PutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}
	var response config.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	wire := findPearTubeConfig(response)
	if wire[config.PearTubeConfigContributionBudget] != "34" ||
		wire[config.PearTubeConfigArchiveBudget] != "233" {
		t.Fatalf("budget wire values = %v, want decimal strings", wire)
	}

	saved, err := manager.Load()
	if err != nil {
		t.Fatalf("load saved settings: %v", err)
	}
	policy := saved.PearTubeConfig()
	if policy.ContributionBudget != 34 || policy.ArchiveBudget != 233 {
		t.Fatalf("budget round-trip = contribution %d archive %d", policy.ContributionBudget, policy.ArchiveBudget)
	}
}

func TestPutSettingsCannotRetainLegacyAutoSeedAsCurrentConsent(t *testing.T) {
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	handler := NewSettingsHandler(manager)
	payload := config.DefaultSettings()
	payload.TorrentScrapers = append(payload.TorrentScrapers, config.TorrentScraperConfig{
		Name:    "PearTube",
		Type:    config.TorrentScraperTypePearTube,
		URL:     "http://relay.internal:8178",
		Enabled: true,
		Config:  map[string]string{"autoSeed": "true"},
	})
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.PutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}
	saved, err := manager.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	policy := saved.PearTubeConfig()
	if policy.ContributeWatchedMedia || policy.ArchiveEnabled || !policy.MigrationRequired {
		t.Fatalf("legacy save granted consent: %+v", policy)
	}
	if _, exists := findPearTubeConfig(saved)["autoSeed"]; exists {
		t.Fatal("legacy autoSeed survived normal save")
	}
}

func TestPutSettingsAppliesPearTubeWithoutRestart(t *testing.T) {
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	handler := NewSettingsHandler(manager)
	pearTube := NewPearTubeHandler(nil)
	handler.SetPearTubeConfigurer(pearTube)
	t.Cleanup(func() { _ = pearTube.ApplyPearTubeSettings(config.PearTubeSettings{}) })

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entities":[],"total":0}`))
	}))
	defer relay.Close()

	putPearTubeSettings(t, handler, config.PearTubeSettings{
		RelayURL:           relay.URL,
		ConsentVersion:     config.PearTubeConsentVersion,
		ContributionBudget: 21,
		ArchiveBudget:      144,
	})
	body := pearTubeStatusBody(t, pearTube)
	if body.State != "ready" || !body.Reachable {
		t.Fatalf("hot reload reachability = %+v", body)
	}
	if body.EffectiveMode != config.PearTubeModeWatchOnly || body.ContributeWatchedMedia || body.ArchiveEnabled {
		t.Fatalf("watch-only policy changed during hot reload: %+v", body)
	}

	putPearTubeSettings(t, handler, config.PearTubeSettings{})
	if body := pearTubeStatusBody(t, pearTube); body.State != "disabled" || body.Enabled {
		t.Fatalf("clearing relay left integration on: %+v", body)
	}
}

func TestPutSettingsWaitsForRelayPolicyAndSurfacesRevocationFailure(t *testing.T) {
	t.Setenv(peartube.CompanionClientEnv, "mediastorm-settings-test")
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))

	policyRequests := make(chan peartube.CompanionNetworkPolicy)
	policyResponses := make(chan int)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v2/policy" {
			http.NotFound(w, r)
			return
		}
		var policy peartube.CompanionNetworkPolicy
		if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		policyRequests <- policy
		status := <-policyResponses
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"policy":{"policyVersion":2}}`))
		} else {
			_, _ = w.Write([]byte(`{"error":{"code":"POLICY_REFUSED","message":"policy refused","field":null}}`))
		}
	}))
	defer relay.Close()

	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settingsHandler := NewSettingsHandler(manager)
	pearTube := NewPearTubeHandler(nil)
	sourceGrants := peartube.NewSourceGrantRegistryFromEnv()
	t.Cleanup(sourceGrants.Close)
	pearTube.SetSourceGrants(sourceGrants)
	settingsHandler.SetPearTubeConfigurer(pearTube)
	t.Cleanup(func() { _ = pearTube.ApplyPearTubeSettings(config.PearTubeSettings{}) })

	enabled := true
	initialApply := make(chan error, 1)
	go func() {
		initialApply <- pearTube.ApplyPearTubeSettings(config.PearTubeSettings{
			RelayURL:               relay.URL,
			Enabled:                &enabled,
			ConsentVersion:         config.PearTubeConsentVersion,
			ContributeWatchedMedia: true,
			ContributionBudget:     21,
			ArchiveBudget:          144,
		})
	}()
	initialPolicy := <-policyRequests
	if !initialPolicy.ContributeWatchedMedia || initialPolicy.ContributionBudgetBytes != 21*bytesPerGiB {
		t.Fatalf("initial policy = %+v", initialPolicy)
	}
	policyResponses <- http.StatusOK
	if err := <-initialApply; err != nil {
		t.Fatalf("initial ApplyPearTubeSettings: %v", err)
	}

	reductionDone := make(chan *httptest.ResponseRecorder, 1)
	reductionBody := pearTubeSettingsBody(t, config.PearTubeSettings{
		RelayURL:               relay.URL,
		Enabled:                &enabled,
		ConsentVersion:         config.PearTubeConsentVersion,
		ContributeWatchedMedia: true,
		ContributionBudget:     10,
		ArchiveBudget:          144,
	})
	go func() {
		rec := httptest.NewRecorder()
		settingsHandler.PutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(reductionBody)))
		reductionDone <- rec
	}()
	reducedPolicy := <-policyRequests
	if !reducedPolicy.ContributeWatchedMedia || reducedPolicy.ContributionBudgetBytes != 10*bytesPerGiB {
		t.Fatalf("reduced policy = %+v", reducedPolicy)
	}
	select {
	case rec := <-reductionDone:
		t.Fatalf("settings returned %d before relay policy response", rec.Code)
	default:
	}
	if _, ok := pearTube.planAutoSeed(moviePlayback()); ok {
		t.Fatal("an automatic source was admitted during policy reconciliation")
	}
	policyResponses <- http.StatusOK
	if rec := <-reductionDone; rec.Code != http.StatusOK {
		t.Fatalf("reduction PUT status %d: %s", rec.Code, rec.Body.String())
	}

	cancelCalls := 0
	pearTube.playbackMu.Lock()
	pearTube.activeAcquisitions = map[string]*autoSeedAcquisition{
		"active": {
			cancel:    func() { cancelCalls++ },
			relay:     pearTube.currentRelay(),
			state:     "acquiring",
			createdAt: time.Now(),
		},
	}
	pearTube.autoSeedJobsByState = map[string]int{"acquiring": 1}
	pearTube.playbackMu.Unlock()
	epochBeforeDisable := sourceGrants.PolicyEpoch()

	disabled := false
	disableDone := make(chan *httptest.ResponseRecorder, 1)
	disableBody := pearTubeSettingsBody(t, config.PearTubeSettings{
		RelayURL: relay.URL,
		Enabled:  &disabled,
	})
	go func() {
		rec := httptest.NewRecorder()
		settingsHandler.PutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(disableBody)))
		disableDone <- rec
	}()
	revocation := <-policyRequests
	if revocation.ContributeWatchedMedia || revocation.ArchiveEnabled ||
		revocation.UploadPermission != "disabled" || revocation.UploadCeilingBytes != 0 {
		t.Fatalf("disable revocation policy = %+v", revocation)
	}
	select {
	case rec := <-disableDone:
		t.Fatalf("settings returned %d before failed relay revocation response", rec.Code)
	default:
	}
	if pearTube.currentRelay() != nil || cancelCalls != 1 || sourceGrants.PolicyEpoch() <= epochBeforeDisable {
		t.Fatalf("local cutover was not fail-closed: relay=%v cancels=%d epoch=%d->%d",
			pearTube.currentRelay(), cancelCalls, epochBeforeDisable, sourceGrants.PolicyEpoch())
	}
	if _, ok := pearTube.planAutoSeed(moviePlayback()); ok {
		t.Fatal("an automatic source was admitted while remote revocation was unresolved")
	}
	policyResponses <- http.StatusInternalServerError
	rec := <-disableDone
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("failed revocation PUT status %d: %s", rec.Code, rec.Body.String())
	}
	if pearTube.currentRelay() != nil || pearTube.contributeWatchedMedia {
		t.Fatal("failed remote revocation restored local contribution")
	}

	retryDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		settingsHandler.PutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(disableBody)))
		retryDone <- rec
	}()
	retryRevocation := <-policyRequests
	if retryRevocation.UploadPermission != "disabled" || retryRevocation.UploadCeilingBytes != 0 {
		t.Fatalf("retried revocation policy = %+v", retryRevocation)
	}
	select {
	case rec := <-retryDone:
		t.Fatalf("retry returned %d before relay revocation response", rec.Code)
	default:
	}
	policyResponses <- http.StatusOK
	if rec := <-retryDone; rec.Code != http.StatusOK {
		t.Fatalf("retried disable PUT status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPearTubeEnvironmentControlsRelayNotConsent(t *testing.T) {
	t.Setenv(peartube.CompanionSharedSecretEnv, strings.Repeat("4d", 32))
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v2/policy" {
			_, _ = w.Write([]byte(`{"policy":{"policyVersion":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"entities":[],"total":0}`))
	}))
	defer relay.Close()
	t.Setenv(peartube.RelayURLEnv, relay.URL)
	t.Setenv("PEARTUBE_AUTOSEED", "1")

	pearTube := NewPearTubeHandler(nil)
	t.Cleanup(func() {
		disabled := false
		_ = pearTube.ApplyPearTubeSettings(config.PearTubeSettings{Enabled: &disabled})
	})
	if err := pearTube.ApplyPearTubeSettings(config.PearTubeSettings{MigrationRequired: true}); err != nil {
		t.Fatalf("ApplyPearTubeSettings: %v", err)
	}
	body := pearTubeStatusBody(t, pearTube)
	if !body.Enabled || !body.FromEnv["relayUrl"] {
		t.Fatalf("relay authority did not come from environment: %+v", body)
	}
	if body.ContributeWatchedMedia || body.ArchiveEnabled ||
		body.EffectiveMode != config.PearTubeModeMigrationRequired {
		t.Fatalf("environment implied consent: %+v", body)
	}
	if body.FromEnv["contributeWatchedMedia"] || body.FromEnv["archiveEnabled"] {
		t.Fatalf("status attributed consent to environment: %+v", body.FromEnv)
	}
}

func TestPearTubeStatusExposesPolicyWithoutLegacyOrControlMaterial(t *testing.T) {
	handler := NewPearTubeHandler(nil)
	if err := handler.ApplyPearTubeSettings(config.PearTubeSettings{
		ConsentVersion:     config.PearTubeConsentVersion,
		ContributionBudget: 21,
		ArchiveBudget:      144,
	}); err != nil {
		t.Fatalf("ApplyPearTubeSettings: %v", err)
	}
	t.Cleanup(func() { _ = handler.ApplyPearTubeSettings(config.PearTubeSettings{}) })

	rec := httptest.NewRecorder()
	handler.Status(rec, httptest.NewRequest(http.MethodGet, "/api/p2p/status", nil))
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	for _, forbidden := range []string{"autoSeed", "controlKey", "capability", "callbackUrl"} {
		if _, exists := body[forbidden]; exists {
			t.Fatalf("status exposed forbidden field %q: %v", forbidden, body)
		}
	}
	if body["effectiveMode"] != config.PearTubeModeWatchOnly || body["budgetUnit"] != "GiB" {
		t.Fatalf("status omitted explicit policy or units: %v", body)
	}
}

func TestPearTubeSchemaExposesSeparateExplicitPolicies(t *testing.T) {
	section, ok := SettingsSchema["torrentScrapers"].(map[string]interface{})
	if !ok {
		t.Fatal("torrentScrapers settings schema is missing")
	}
	fields, ok := section["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("torrentScrapers section has no fields")
	}
	typeField := fields["type"].(map[string]interface{})
	options := typeField["options"].([]string)
	if !slices.Contains(options, config.TorrentScraperTypePearTube) {
		t.Fatalf("peartube is not an offered scraper type: %v", options)
	}

	for _, fieldKey := range []string{
		"config.contributeWatchedMedia",
		"config.contributionBudget",
		"config.archiveEnabled",
		"config.archiveBudget",
	} {
		field, ok := fields[fieldKey].(map[string]interface{})
		if !ok {
			t.Fatalf("%s missing from scraper schema", fieldKey)
		}
		if !showsForPearTube(field) {
			t.Fatalf("%s hidden for peartube", fieldKey)
		}
	}
	if _, exists := fields["config.autoSeed"]; exists {
		t.Fatal("legacy autoSeed control is still exposed")
	}

	pearTube := NewPearTubeHandler(nil)
	t.Cleanup(func() { _ = pearTube.ApplyPearTubeSettings(config.PearTubeSettings{}) })
	if err := pearTube.ApplyPearTubeSettings(config.PearTubeSettings{MigrationRequired: true}); err != nil {
		t.Fatalf("ApplyPearTubeSettings: %v", err)
	}
	body := pearTubeStatusBody(t, pearTube)
	if body.Enabled || body.State != "disabled" || body.EffectiveMode != config.PearTubeModeMigrationRequired {
		t.Fatalf("disabled status is not truthful: %+v", body)
	}
}

func findPearTubeConfig(settings config.Settings) map[string]string {
	for _, scraper := range settings.TorrentScrapers {
		if scraper.Type == config.TorrentScraperTypePearTube {
			return scraper.Config
		}
	}
	return nil
}

// showsForPearTube reports whether a scraper field is visible once the type is
// peartube, reading the same showWhen the client renders from.
func showsForPearTube(field map[string]interface{}) bool {
	when, ok := field["showWhen"].(map[string]interface{})
	if !ok {
		return true // no condition means always shown
	}
	if when["field"] == "type" && when["value"] == config.TorrentScraperTypePearTube {
		return true
	}
	conditions, ok := when["conditions"].([]map[string]interface{})
	if !ok {
		return false
	}
	for _, condition := range conditions {
		if condition["field"] == "type" && condition["value"] == config.TorrentScraperTypePearTube {
			return true
		}
	}
	return false
}

type pearTubeStatus struct {
	Enabled                bool            `json:"enabled"`
	State                  string          `json:"state"`
	Reachable              bool            `json:"reachable"`
	ContributeWatchedMedia bool            `json:"contributeWatchedMedia"`
	ContributionBudget     int             `json:"contributionBudget"`
	ArchiveEnabled         bool            `json:"archiveEnabled"`
	ArchiveBudget          int             `json:"archiveBudget"`
	ConsentVersion         int             `json:"consentVersion"`
	MigrationRequired      bool            `json:"migrationRequired"`
	EffectiveMode          string          `json:"effectiveMode"`
	Remedy                 string          `json:"remedy"`
	FromEnv                map[string]bool `json:"fromEnv"`
	JobsByState            map[string]int  `json:"jobsByState"`
}

func pearTubeStatusBody(t *testing.T, handler *PearTubeHandler) pearTubeStatus {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.Status(rec, httptest.NewRequest(http.MethodGet, "/admin/api/p2p/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body pearTubeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return body
}
