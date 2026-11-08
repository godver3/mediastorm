package debrid

import "strings"

// AddonStreamErrorMessage reports whether a Stremio stream entry is an addon's
// in-band error notice rather than a playable stream, and returns the
// human-readable error text when it is.
//
// Stremio addons answer misconfigured requests with HTTP 200 and a fake
// "stream" carrying the error: AIOStreams sends "[❌] AIOStreams" with the
// message in its description and no playable url (for example when the URL
// is the server address instead of the /stremio/<uuid>/<config> manifest
// URL); Comet sends "[⚠️] Comet" whose url is just its homepage. Counting that
// entry made admin tests report success, and dropping it silently made every
// search return 0 results with no reason given.
func AddonStreamErrorMessage(name, description, streamURL, infoHash, streamType string) (string, bool) {
	isError := strings.EqualFold(strings.TrimSpace(streamType), "error") ||
		strings.Contains(name, "❌") ||
		strings.HasPrefix(strings.TrimSpace(name), "[⚠")
	if !isError {
		return "", false
	}
	// A real torrent entry is never an error notice, whatever its badge says.
	if strings.TrimSpace(infoHash) != "" {
		return "", false
	}
	// "❌"/"⚠" badges only mark an error when the entry is not a playable
	// file link; an explicit error type always does.
	if !strings.EqualFold(strings.TrimSpace(streamType), "error") && looksLikeMediaURL(streamURL) {
		return "", false
	}
	msg := strings.Join(strings.Fields(description), " ")
	if msg == "" {
		msg = strings.Join(strings.Fields(name), " ")
	}
	if msg == "" {
		msg = "unknown addon error"
	}
	return msg, true
}

func looksLikeMediaURL(rawURL string) bool {
	u := strings.ToLower(strings.TrimSpace(rawURL))
	if u == "" {
		return false
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	for _, ext := range []string{".mkv", ".mp4", ".m4v", ".avi", ".webm", ".ts", ".m2ts", ".mov", ".m3u8"} {
		if strings.HasSuffix(u, ext) {
			return true
		}
	}
	// Debrid/addon playback links are usually extension-less resolver paths
	// (…/playback/…, …/resolve/…, …/stream/…); a bare host or homepage is not.
	for _, seg := range []string{"/playback/", "/resolve/", "/stream/", "/download/", "/dl/"} {
		if strings.Contains(u, seg) {
			return true
		}
	}
	return false
}
