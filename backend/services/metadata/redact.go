package metadata

import (
	"net/http"

	"novastream/internal/urlredact"
)

// doRedacted performs the request and strips API keys carried in the request
// URL (query-string keys) from any transport error before it is logged or
// returned.
func doRedacted(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, urlredact.Error(err)
	}
	return resp, nil
}
