# Visible episode source checks

Run from `backend`:

```sh
go test ./services/sourceavailability ./handlers ./api ./services/indexer
go test -race ./services/sourceavailability -run '^TestEpisode' -count=1
```

The endpoint is `PUT /api/users/{userID}/source-availability/episodes/{viewID}`.
It requires a profile session, client identity, a view ID and a positive ordered
version. Bodies contain `titleId`, metadata `numbering`, and up to 12 episode
coordinates (`episodeId`, `seasonNumber`, `episodeNumber`). They never accept a
provider query or release name. An empty `episodes` array releases that view
without loading metadata. Replies are private and `no-store`.

The handler resolves metadata again under the profile, verifies every ID and
coordinate, and preserves the selected catalogue title separately from the
provider numbering. Foreign accounts, locked profiles and source-scoped share
sessions cannot create work. Child profiles without established rating-based
authorization remain closed; list-based child authorization needs another path.

Only explicitly visible episode requests enter the existing quota-limited
worker. Views expire after 30 seconds without renewal, are limited to 16 per
profile and 128 globally, and retain short tombstones against late updates.
Versions must increase; stale updates return 409. Capacity failures return 429.
The last departing consumer cancels its active search. A second visible view of
the same selection keeps that search alive. A refresh or a different view cannot
reset the persisted provider budget. Title scheduling also requires explicit visible demand; see `title-testing.md`.

Cache identities include the selected series, numbering, season, episode and
metadata provenance. Evidence is re-evaluated with the episode-specific report
context. A named single episode can establish a candidate; other episodes do
not. Unresolved season packs, ranges, unknown title aliases and unverified
alternate orderings remain unknown. Date-named daily releases require the exact
verified air date, unique across complete season metadata. Duplicate dates or
incomplete season metadata stay unknown. Absolute-only and alternate-order
releases remain unknown. Unconfirmed air dates cannot establish a negative result.
No NZB download, media decoding or file-list resolution is performed.

The native observer sends actual clipped visible items while its row is resumed
and uncovered by playback or preparation.
It reuses one view ID across scroll changes, orders cleanup before later updates,
limits the first fast polls and then renews every 15 seconds. Unsupported servers
and failed requests keep normal card behavior. A listed local copy prevents a
remote negative from being presented as verified total absence, but is not proof
of a usable local audio track. Native rendering and physical-device acceptance
remain separate checks.
