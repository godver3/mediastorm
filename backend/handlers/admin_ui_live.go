package handlers

// liveVisibleFields are the server-wide Live TV settings the admin page shows in the
// "Live TV Playback" section. The section's other fields are the single-source settings from
// before live.sources: still read for older configs, but edited per source now.
var liveVisibleFields = map[string]bool{
	"timeshiftMinutes": true,
	"comskip.enabled":  true,
	"comskip.path":     true,
	"comskip.iniPath":  true,
}

func init() { hideLegacyLiveFields(SettingsSchema) }

// hideLegacyLiveFields marks every field of the "live" section hidden except liveVisibleFields,
// and drops its Test Connection button: it tested those hidden single-source fields, so with
// only named sources (each with its own test) it failed with "Playlist URL is required".
func hideLegacyLiveFields(schema map[string]interface{}) {
	section, _ := schema["live"].(map[string]interface{})
	if section == nil {
		return
	}
	delete(section, "testable")
	fields, _ := section["fields"].(map[string]interface{})
	for key, def := range fields {
		if field, ok := def.(map[string]interface{}); ok && !liveVisibleFields[key] {
			field["hidden"] = true
		}
	}
}
