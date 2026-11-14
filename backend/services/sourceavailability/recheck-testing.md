# Explicit source checks

`POST /api/users/{userID}/source-availability/recheck` accepts a catalogue title
coordinate and optionally one exact server-resolved episode/numbering. It never
accepts search text, release names, URLs, permission flags or budget overrides.
Title metadata must have been delivered to the same profile/client. Episode
metadata is resolved and authorized exactly as for visible episode demand.
Ownership, PIN, kids restrictions and current playback context remain enforced.

A 202 response acknowledges queued work, not a source result. Duplicate pending
requests share the same job without extending its deadline. A completed request
has a one-minute cooldown per profile/title-or-episode even across client or
language changes; rejected attempts return 429 and Retry-After. At most four
requests per profile and 32 globally can be pending, in a bounded ledger.

Explicit work has priority over automatic visible demand and survives viewport
departure for at most two minutes. Foreground searches still have priority.
Cancellation may resume a still-needed explicit request. Fresh cached results
are deliberately rechecked, but the persisted provider budget is never reset.
A fresh local check runs first and can save the provider query. Missing provider,
exhausted budget, timeout and changed context remain unknown with distinct retry
reasons. Queue state temporarily masks old evidence; it never records absence.
Job generations prevent an older completion from overwriting a newer action.
Jobs/cooldowns are in memory; the provider budget survives server restart.

TV and phone title/episode menus expose the action only when source hints are
supported. Menu descriptions reuse bounded last-known evidence and expiry
handling without creating demand. A successful request immediately refreshes
currently visible observations, using the same ordered view identity. Hidden
views stay released. Window focus stops episode observation behind a dialog.
Profile/server changes discard late replies. Unnumbered episodes and library
cards cannot issue a generic title recheck.

Run the 12 affected backend packages, focused sourceavailability race tests,
backend build, all three native unit-test modules and both debug APK builds.
Opt-in TestSourceRecheckBrowser drives real HTTP/worker requests with synthetic
provider results, verifies duplicate requests, fresh-cache replacement, cooldown,
budget exhaustion and desktop/mobile fixture layout. The fixture is not native
Compose rendering, actual provider playback or physical remote-control input.
No private playlist, provider credentials or user media is used.
