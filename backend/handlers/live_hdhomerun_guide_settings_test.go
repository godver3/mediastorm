package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestHDHomeRunGuideSettingsSchema(t *testing.T) {
	for _, schema := range []map[string]interface{}{SettingsSchema} {
		// Schema names differ between global and profile settings.
		found := 0
		for _, name := range []string{"live", "live.sources", "liveTV", "liveTV.sources"} {
			section, ok := schema[name].(map[string]interface{})
			if !ok {
				continue
			}
			fields := section["fields"].(map[string]interface{})
			for _, key := range []string{"hdhomerunGuideEmail", "hdhomerunGuideDeviceIds"} {
				field, ok := fields[key].(map[string]interface{})
				if !ok {
					t.Fatalf("missing field %s.%s", name, key)
				}
				condition := field["showWhen"].(map[string]interface{})
				if field["type"] != "text" || field["required"] == true || condition["field"] != "mode" || condition["value"] != "hdhomerun" {
					t.Fatalf("incorrect HDHomeRun guide field: %s.%s", name, key)
				}
			}
			found++
		}
		if found != 4 {
			t.Fatalf("expected global/profile top-level and per-source guide settings, got %d", found)
		}
	}
}

func TestHDHomeRunGuideSettingsRedactedAndPreserved(t *testing.T) {
	source := config.LivePlaylistSource{HDHomeRunGuideEmail: "source@example.com", HDHomeRunGuideDeviceIDs: "10BFFFFF"}
	original := config.Settings{Live: config.LiveSettings{HDHomeRunGuideEmail: "viewer@example.com", HDHomeRunGuideDeviceIDs: "10AFFFFF", Sources: []config.LivePlaylistSource{source}, PlaylistSources: []config.LivePlaylistSource{source}}}
	raw, _ := json.Marshal(original)
	var incoming config.Settings
	if err := json.Unmarshal(raw, &incoming); err != nil {
		t.Fatal(err)
	}
	redactSettings(&incoming)
	raw, _ = json.Marshal(incoming)
	for _, value := range []string{"viewer@example.com", "source@example.com", "10AFFFFF", "10BFFFFF"} {
		if strings.Contains(string(raw), value) {
			t.Fatal("settings response exposes guide account identifiers")
		}
	}
	preserveRedactedFields(&incoming, &original)
	if incoming.Live.HDHomeRunGuideEmail != original.Live.HDHomeRunGuideEmail || incoming.Live.HDHomeRunGuideDeviceIDs != original.Live.HDHomeRunGuideDeviceIDs || incoming.Live.Sources[0].HDHomeRunGuideEmail != source.HDHomeRunGuideEmail || incoming.Live.PlaylistSources[0].HDHomeRunGuideDeviceIDs != source.HDHomeRunGuideDeviceIDs {
		t.Fatal("saving masked settings lost guide authentication")
	}
	next := original
	next.Live.HDHomeRunGuideEmail = "changed@example.com"
	if epgConfigFingerprint(original) == epgConfigFingerprint(next) {
		t.Fatal("account email change did not trigger guide refresh")
	}
	mode, email, ids := "hdhomerun", "viewer@example.com", "10AFFFFF"
	previous := models.LiveTVSettings{Mode: &mode, HDHomeRunGuideEmail: &email, HDHomeRunGuideDeviceIDs: &ids}
	other := "10BFFFFF"
	changed := previous
	changed.HDHomeRunGuideDeviceIDs = &other
	if !liveGuideSourcesChanged(previous, changed) {
		t.Fatal("profile guide device change did not trigger refresh")
	}
}
