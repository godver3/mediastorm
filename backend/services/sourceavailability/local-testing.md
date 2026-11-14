# Local source evidence

The existing visibility worker can inspect a matching scanned local file before
querying a provider. It creates no download, playback session or second buffer.
File/track probes share playback authorization, exact match, size, resolution,
HDR, unknown-track and report filtering. There is one serial visibility worker,
a three-second local wait and at most two matching candidates per check. A local
success requires no provider request or provider-budget reservation.

Local hints are private per profile/selection/episode and live in bounded memory
(maximum 500). Successful file and requested-audio evidence lasts two minutes;
probe/catalogue errors remain unknown and retry after 30 seconds while visible.
No filesystem path, filename, local item ID or proof fingerprint enters the hint.
Only `sourceType: local` and `reason: local_file_checked` distinguish the result.
Each visible reply rechecks current account/library rights, catalogued identity
and playback preferences under one 500 ms metadata-only deadline. A cached empty
local result is also rechecked so new files or grants cannot reuse old absence.
No filesystem call or media probe runs in this reply validation.

The local check bypasses the two-hour shared playback-probe cache, then makes the
fresh result available for normal playback reuse. Native clients accept local
markers only with matching reason/state and a freshness window of two minutes.
A checked file/track is not guaranteed playable media, verified spoken language,
healthy samples or accurate synchronization. Unscanned filesystem changes between
checks still need the next check or an actual playback attempt.

Ordinary catalogue responses remain cache-only and suppress a remote negative
until current local evidence is established through explicit visibility. Older
clients therefore remain conservative. Exact episode hints can check a manually
matched local daily file even when a date-named remote release is unconfirmed.
Unverified alternate numbering stays unknown; one file never establishes a whole
series. A local source without any configured indexer still works.

Local preparations do not create a generic remote positive. New remote
preparations carry an internal marker; older ambiguous preparation records must
be rechecked. Revoked local access cannot keep an old prepared positive alive.
Reports and their undo invalidate contextual local proof without globally
excluding another language or every episode.

Run the 12 affected backend packages, the focused race suite, both native unit
suites/builds and opt-in `TestLocalSourceHintsBrowser`. The browser uses real HTTP
handlers, the worker and FFprobe on generated video/audio, toggles library access
and switches French/Spanish intent. It verifies API behavior and layout, not
native Compose rendering, decoder playback or actual spoken-language detection.
No existing user file or private playlist is used. Real device acceptance remains
before release.
