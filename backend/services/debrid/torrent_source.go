package debrid

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Indexer download URLs can return torrent metainfo or redirect to a magnet.
type torrentSource struct {
	data     []byte
	filename string
	magnet   string
}

func (s torrentSource) add(ctx context.Context, provider Provider) (*AddMagnetResult, error) {
	if s.magnet != "" {
		log.Printf("[debrid] adding redirected magnet to %s", provider.Name())
		return addMagnetUnlessUncached(ctx, provider, s.magnet)
	}
	infoHash, _ := torrentV1InfoHash(s.data)
	if err := recentlyUncachedError(provider.Name(), infoHash); err != nil {
		return nil, err
	}
	log.Printf("[debrid] uploading torrent file (%d bytes) to %s", len(s.data), provider.Name())
	result, err := provider.AddTorrentFile(ctx, s.data, s.filename)
	if err == nil {
		noteAddResult(provider.Name(), infoHash, result)
	}
	return result, err
}

func downloadTorrentSource(ctx context.Context, torrentURL string, timeout time.Duration) (torrentSource, error) {
	var source torrentSource
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if strings.EqualFold(req.URL.Scheme, "magnet") {
				source.magnet = req.URL.String()
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, torrentURL, nil)
	if err != nil {
		return torrentSource{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return torrentSource{}, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if source.magnet != "" {
		if extractInfoHashFromMagnet(source.magnet) == "" {
			return torrentSource{}, fmt.Errorf("magnet redirect has no supported info hash")
		}
		return source, nil
	}
	if resp.StatusCode != http.StatusOK {
		return torrentSource{}, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	source.data, err = io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return torrentSource{}, fmt.Errorf("read response: %w", err)
	}
	if len(source.data) < 10 || source.data[0] != 'd' {
		return torrentSource{}, fmt.Errorf("invalid torrent file format (expected bencoded data)")
	}
	source.filename = extractTorrentFilename(resp, torrentURL)
	return source, nil
}

// extractTorrentFilename tries to get a filename for the torrent file.
func extractTorrentFilename(resp *http.Response, torrentURL string) string {
	// Try Content-Disposition header first
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if strings.Contains(cd, "filename=") {
			parts := strings.Split(cd, "filename=")
			if len(parts) >= 2 {
				filename := strings.Trim(parts[1], `"' `)
				if filename != "" {
					return filename
				}
			}
		}
	}

	// Try to extract from URL path
	if parsed, err := url.Parse(torrentURL); err == nil {
		filename := path.Base(parsed.Path)
		if filename != "" && filename != "." && filename != "/" {
			if !strings.HasSuffix(strings.ToLower(filename), ".torrent") {
				filename += ".torrent"
			}
			return filename
		}
	}

	return "download.torrent"
}
