package ytdlp

import "strings"

// AppendProxyArgs adds yt-dlp proxy arguments when a proxy URL is configured.
func AppendProxyArgs(args []string, proxyURL string) []string {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return args
	}
	return append(args, "--proxy", proxyURL)
}
