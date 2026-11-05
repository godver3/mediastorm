package handlers

// liveVisibleFields are the server-wide Live TV settings the admin page shows in the
// "Live TV Playback" section. The section's other fields are the single-source settings from
// before live.sources: still read for older configs, but edited per source now.
var liveVisibleFields = map[string]bool{
	"timeshiftMinutes": true,
}

func init() { hideLegacyLiveFields(SettingsSchema) }

// hideLegacyLiveFields marks every field of the "live" section hidden except liveVisibleFields.
func hideLegacyLiveFields(schema map[string]interface{}) {
	section, _ := schema["live"].(map[string]interface{})
	fields, _ := section["fields"].(map[string]interface{})
	for key, def := range fields {
		if field, ok := def.(map[string]interface{}); ok && !liveVisibleFields[key] {
			field["hidden"] = true
		}
	}
}
