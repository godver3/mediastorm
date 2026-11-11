# Vendored nntppool v2.3.2 (patched)

Copy of `github.com/javi11/nntppool/v2@v2.3.2` (tests and fixtures removed),
wired in via the `replace` directive in `backend/go.mod`.

Patch: `pkg/nntpcli/connection.go` `Ping()` expects `111` for `DATE`
(RFC 3977 §7.1). Upstream expects `200`, so the pool's provider health check
fails on every compliant server and marks all providers offline every cycle.

Patch: `pkg/nntpcli/decoder.go` `incrementalDecoder` stopped at the yEnc
`\r\n=y` control sequence (`rapidyenc.EndControl`) and never consumed the rest
of the `=yend ...` line or the `.\r\n` terminator. When the trailer arrived in
a later TCP read it stayed on the connection, so the next BODY on that pooled
connection failed with `invalid response code: end size=...` / `short
response:` and slow segments stalled playback into stream migration. The
decoder now drains through the terminator (`drainToTerminator`) and surfaces a
drain failure so the connection is discarded. Test:
`pkg/nntpcli/decoder_drain_test.go`.

Drop this directory and the `replace` once upstream ships a fix.
