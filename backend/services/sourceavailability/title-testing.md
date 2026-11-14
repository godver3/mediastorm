# Visible catalogue title checks

`PUT /api/users/{userID}/source-availability/titles/{viewID}` accepts a positive
ordered `version` and an explicit `titles` array of up to 24 `titleId` and
`mediaType` pairs. An empty array releases the view. Query text is not accepted.
Profile ownership, PIN and session scope are checked first. Child profiles need
current rating-based authorization; list-based authorization remains closed.

Serving catalogue/watchlist responses reads evidence and registers a bounded
search projection. It never queues a source check, even on expiry. Only explicit
visible demand reaches the worker and existing persisted provider budget. Title
and episode views share 30-second leases, ordered versions and capacity limits
of 16 per profile and 128 globally. The last consumer cancels pending/running
work. Hidden/canceled work cannot commit negative evidence. Changed preferences
need fresh demand instead of requeueing an old selection.

Metadata projections belong to one profile and client, expire after 30 minutes
without active renewal and are bounded to 2000 globally. They exclude artwork,
stream/download addresses, provider attributes and full descriptions. Missing or
expired projections, including after server restart, return unknown with reason
`catalogue_refresh_required` until the catalogue is reloaded. There is no metadata
or provider I/O in a lease update. Older clients still read cached hints but do
not schedule new background work; matching native changes enable renewed hints.

TV and phone cards share one observer; duplicate shelves do not create duplicate
demand. Only clipped in-window, resumed, focused cards outside playback/preparation
are submitted, up to 24 unique titles. Episode, action and direct-library cards do
not inherit series evidence. Menus reuse current evidence without another view.
Failed/unsupported endpoints keep ordinary actions and unknown source state.

Run the affected service, handler, API and playback packages and `-race` for the
visibility cases. Opt-in `TestVisibleTitleHintsBrowser` uses
`MEDIASTORM_BROWSER_PYTHON` and `MEDIASTORM_CHROMIUM`: real HTTP handlers and worker,
synthetic providers, browser intersection/scroll events, hidden cards, revisit and
desktop/mobile layout. This is an API fixture, not native rendering or playback.
Physical TV/phone focus and accessibility acceptance remain before release.
