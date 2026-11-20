# NZB acquisition budget

Set `NZB_TITLE_BUDGET_ENABLED=true` in the server environment to enable the
optional download budget. The standard Compose file forwards this variable.
The default is disabled. No companion service or extra datastore is required.

With the budget enabled, playback, availability checks and track probes use one
Go acquisition service. PostgreSQL records each outgoing attempt before HTTP
starts. For a selected movie or catalog episode, the rolling ten-minute window
allows one attempt per release (indexer plus exact download URL) and three in
total, with at least thirty seconds between attempts. Another release from the
same indexer can therefore still be tried as a fallback. A denied request returns an error immediately with
the next eligible time. Attempts that return HTTP or transport errors still
count, because the indexer may already have charged the grab.

Search captures the selected TMDB/TVDB identity before generating localized,
mapped, daily or season-pack fallback queries. It places `nzbCatalogId`,
`nzbMediaType`, `nzbSeason`, `nzbEpisode` and `nzbOrdering` in each result's existing `attributes`
map. Preserve these attributes when sending the result to resolve or health
endpoints. For movies, use a catalog ID such as `tmdb:movie:78` and media type
`movie`, with season and episode zero. For series, use `tmdb:tv:42` or
`tvdb:series:42`, media type `series`, and the selected catalog season and episode
(including season zero for specials). Series coordinates use the catalog that
owns the episode numbering; non-official orderings have separate keys. Profiles and release names are never
part of the budget key. A request without a valid catalog identity (a free-text
or absolute/daily-numbered query without SxxEyy, a movie known only by IMDb ID)
is budgeted per release instead: one grab of that release per window. There is
no release-name normalization or implicit alias matching between separate
catalog IDs.

Concurrent requests for the same indexer and exact download URL share one grab.
Successful NZB bytes are reused for ten minutes for any title, so a season pack
fetched for one episode serves the next episode without another grab. The cache
evicts the oldest entries first and is bounded by 256 entries and 64 MiB for the whole process. One waiter's cancellation does not cancel the
other waiters. Different titles download independently; database locks cover only
the brief reservation transaction. HTTP requests have a thirty-second timeout and
a fifty-MiB response limit. Any 2xx is a success; incomplete, oversized and empty bodies are errors. Error
messages name the indexer, the download URL without credentials or query
string, and the start of the indexer's error body.

All three download paths use the existing per-indexer User-Agent override and
default NZB download headers. HTTP 429, and HTTP 503 with a valid `Retry-After`,
open the existing process-wide `providerbreaker`, protecting searches as well as
downloads. Numeric seconds and HTTP-date retry hints can extend its exponential
cooldown up to twenty-four hours (also applied to pauses restored at startup);
subsequent shorter hints cannot reduce an existing pause. A 401, 404 or ordinary transport/body error does not pause the
indexer. Successful cached NZBs remain usable during an indexer pause.

When enabled, budget attempts and breaker cooldowns are stored in the existing
Postgres database and restored at startup. Only NZB bytes and in-flight requests
are process-local: restarting can lose a cached payload but cannot reset its
budget or a saved pause. Database reservation failures stop the grab; startup
fails if saved pauses cannot be read. A failed cooldown write is logged and the
current process remains paused, but that failed write cannot protect a later
process until PostgreSQL is available again. Expired attempt rows are removed
on subsequent reservations. Expired cooldowns retain their backoff count until a
half-open probe succeeds.

Migration `071_nzb_acquisition.sql` adds two tables. Disabling the flag restores
unbudgeted acquisition and leaves these rows intact. The shared download path,
download headers and providerbreaker still apply with the flag disabled. Keep
the Postgres volume in normal backups. No live deployment is needed to review
this change, and tests use only fixture HTTP responses and a separate database.

Run the focused tests from `backend`:

```sh
go test ./models ./internal/nzbfetch ./internal/providerbreaker
go test ./services/indexer ./services/playback ./services/usenet
go test ./handlers -run 'TestNZB|TestUsenet|TestPrequeue'
NZB_POLICY_TEST_DATABASE_URL='<isolated PostgreSQL DSN>' go test ./internal/datastore -run TestNZBAcquisitionPostgres -count=1
```

The Postgres test creates and removes a private schema, applies all migrations
twice, and checks concurrent reservations, title isolation, expiration and
restart behavior. Without the test DSN it is skipped.
