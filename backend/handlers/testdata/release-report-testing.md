# Release report tests

Run `go test ./handlers ./services/badstreams ./services/playback` from `backend`.
The tests create their own report registry and synthetic sources. No configured
provider, private playlist or existing user media file is used.

`TestReportedAudioReplacementUsesRealMediaProbe` generates a short video with
FFmpeg and probes it using the real video handler. It checks that the reported
track is skipped, the requested language is retained, and native player track
positions still describe the original container. It skips when FFmpeg or FFprobe
is unavailable.

The optional browser test is `TestReleaseReportPlayerBrowser`. Set
`MEDIASTORM_BROWSER_PYTHON` to a Python interpreter with Playwright installed and
`MEDIASTORM_CHROMIUM` to a Chromium executable. Set `MEDIASTORM_BROWSER_ARTIFACTS`
to an output directory for screenshots and the result JSON. It runs the real
player dialog and report/retry handlers with a deterministic media transport
adapter; it does not verify browser media decoding or provider playback.

The browser checks report-only, language isolation, undo, transient manual
retry, replacement after reporting, reset to the beginning for wrong content,
and desktop/mobile layout. Track preferences and language-scoped reports are
also tested with French, Spanish, English, Japanese and German. A per-title
English choice overrides a French or Spanish profile and is unaffected by a
report for the other language.

For a separate synthetic preview, set `MEDIASTORM_RELEASE_REPORT_PREVIEW_ADDR`
and run `go test ./handlers -run '^TestReleaseReportPlayerPreview$' -count=1 -v`.
The root URL opens the fixture player. Media URLs in this preview are synthetic
and do not contain playable user files. The fixture server is opt-in and is not
registered by the application.

## Prepared web replacements

`TestWebPreparedReplacementRules` runs `release_report_rules.test.cjs` with the
Node.js test runner when Node.js is available. Its 21 cases exercise the actual
versioned player asset in an isolated DOM/API fixture: ownership of delayed
responses, shared preparation IDs, explicit adoption, all resume modes, current
position at adoption, scope forwarding, cancellation, failed preference saves,
watch-party/share restrictions and failed startup.

The browser suite now requires a separate confirmation after preparation. It
checks beginning/current/ten-seconds-back, wrong-content reset, real backend
cleanup and a real shared replacement ID returned to two overlapping requests.
The delayed first response cannot delete the preparation owned by the second.
It also checks that a simulated transport startup failure keeps its error and
undo visible. The media transport remains stubbed; successful source preparation
does not establish decoder playback or maintain a second video buffer.

Use `go test ./handlers -run 'TestWebPreparedReplacementRules|TestReleaseReportPlayerBrowser' -count=1`.
A persistent synthetic preview can be started with
`go test ./handlers -run '^TestReleaseReportPlayerPreview$' -count=1 -v -timeout=0`
and the same preview-address environment variable. Do not point the fixture tests
at a normal server.
