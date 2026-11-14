# Explicit profile title ratings

`GET` and `PUT /api/users/{profile}/ratings/{movie|series}/{titleId}`
read, replace or clear the profile's preference. The body and reply contain only
`rating`: `like`, `love`, `dislike` or `none`. A full account session, owned
unlocked profile and `X-Client-ID` are required. The title must have been delivered
to that profile/client during the previous thirty minutes. The bounded catalogue
projection is independent of source hints and is never persisted.

Migration 071 stores preferences permanently in PostgreSQL. A profile deletion
cascades to its votes. Verified provider aliases converge under a transaction
lock per profile. There is no implicit external rating sync, history entry,
watchlist edit or hidden-title marker.

Likes and loves remain recommendation seeds after playback history ages out.
Love has stronger weight; dislikes override implicit history/progress/list seeds
and exclude matching provider identities from recommendation candidates. Matching
never uses only a title's display name. The existing similar-title engine requires
a verified TMDB identity to generate positive recommendations; preferences with
only other provider IDs remain stored and usable for matching and exclusion.
Current child rating caps apply again to writes and seeds. Curated-list child
profiles remain unavailable for unrestricted recommendation/rating actions.

The service, handler and PostgreSQL integration tests cover independent profiles,
changed child restrictions, stale projections, strict bodies, persistent provider
aliases, concurrent updates, rollback, clearing and cascade cleanup.

Run the opt-in database test only against an explicitly disposable Unix-socket
PostgreSQL fixture using `TITLE_RATINGS_TEST_DATABASE_URL`. Its fixture role must
start with `synthetic_`; it creates and removes only its own uniquely named test
databases. Both fresh migration and upgrade from version 70 are exercised.

The full application browser setup and safety requirements are in
[`../family-app/README.md`](../family-app/README.md). With those environment
variables configured, `go test ./handlers -run TestPersonalRatingsApplicationBrowser
-count=1` runs Chromium requests against the actual demo application. It verifies
all preference values, profile/PIN/client boundaries, catalogue annotations,
Home revisions and unchanged history/progress/watchlist/hidden state. It restores
the fixture's previous preference afterward. No response credentials or user IDs
are written to the result artifact.

`application-results.json` is a saved synthetic application run. This is API
coverage through Chromium; it does not render native Compose rating controls.
Those controls require the separate TV/phone unit suites and APK builds, with
physical-device focus, large-font and accessibility acceptance still outstanding.

[`preview.html`](preview.html) links the saved application results and actual
desktop/mobile web-player screenshots. The full browser regression also opens
and closes the real mobile audio/subtitle sheet and checks its viewport bounds
after CSS transitions settle. None of these images depicts native rating UI.
