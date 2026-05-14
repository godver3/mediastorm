package httpheaders

import "net/http"

const (
	UserAgent = "mediastorm/1.0"

	acceptIndexerSearch = "application/xml, application/rss+xml, text/xml, */*"
	acceptNZBDownload   = "application/x-nzb, application/xml, text/xml, */*"
)

func SetIndexerSearchHeaders(req *http.Request) {
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", acceptIndexerSearch)
}

func SetNZBDownloadHeaders(req *http.Request) {
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", acceptNZBDownload)
}
