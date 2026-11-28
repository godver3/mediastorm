package propfind

import "mime"

// Register common media types so Content-Type does not depend on the host's
// /etc/mime.types (absent in minimal containers). The mime registry is
// process-wide, so this also covers the WebDAV adapter's GET responses.
func init() {
	for ext, typ := range map[string]string{
		".mkv":  "video/x-matroska",
		".mp4":  "video/mp4",
		".m4v":  "video/x-m4v",
		".ts":   "video/mp2t",
		".avi":  "video/x-msvideo",
		".webm": "video/webm",
		".srt":  "application/x-subrip",
		".vtt":  "text/vtt",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}
