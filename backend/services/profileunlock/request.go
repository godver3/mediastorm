package profileunlock

import (
	"net/http"
	"strings"
)

// Header carries profile unlock tokens issued by POST /api/users/{id}/pin/verify.
// Several tokens may be comma-separated.
const Header = "X-Profile-Unlock"

// QueryParam is the query-string form for media/stream URLs whose consumers
// cannot attach headers (mirrors ?token=).
const QueryParam = "profileUnlock"

// RequestTokens collects unlock tokens from the header (comma-separated,
// repeatable) and the query parameter.
func RequestTokens(r *http.Request) []string {
	var tokens []string
	collect := func(values []string) {
		for _, value := range values {
			for _, part := range strings.Split(value, ",") {
				if part = strings.TrimSpace(part); part != "" {
					tokens = append(tokens, part)
				}
			}
		}
	}
	collect(r.Header.Values(Header))
	collect(r.URL.Query()[QueryParam])
	return tokens
}
