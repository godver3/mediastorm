# Playback completion evidence

The paired native client provides optional ratings and explicit next choices on
TV and phone. The backend supplies a trusted completion context and current
profile recommendations; the embedded player has its own production dialog.

`player-results.json` covers the rendered production template with generated
video and synthetic API responses. `application-results.json` checks actual
application APIs through Chromium against a verified isolated loopback demo and
synthetic PostgreSQL fixture. The demo intentionally has no external metadata
credentials, so unavailable details fall back to a real server catalogue
response. Handler tests separately cover successful trusted details delivery.
`full-application-results.json` retains the existing login, PIN and generated
video playback path. No real playlist or provider credentials are used.

`backend-results.json` records the complete Go suite, including explicit skips
and both PostgreSQL migration fixtures. `race-results.json` records affected
race checks; `-ldflags=-w` omits DWARF only. `native-results.json` records native
tests and both APK hashes, with private debug defaults confirmed empty.

The PNGs are real web screenshots at 390 and 1280 pixels. They are not rendered
native screens. Physical device rendering, remote focus, large text,
accessibility, picture-in-picture return and decoded audio remain pending.
The backend's completion projection is bounded and process-local. Missing or
expired context requires reopening the title instead of trusting query metadata.
