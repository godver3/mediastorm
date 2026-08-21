# Usenet Latency Opportunities

> **▼ READ FIRST ▼** — working notes from the 2026-08-20 latency instrumentation session.
> Everything an agent needs to measure, verify, and iterate on the items below lives
> in this section. Read it before touching code.

---

## ▶ SESSION HANDOFF (2026-08-21, end of session — read this FIRST)

**Status:** OPP-1, OPP-2, **OPP-3** implemented + verified (tests, race-detector).
Benchmark harness built (backend-side ▶ bench + curl `latency_bench.sh`). Latency
page has per-row ⚡ (copy curl cmd) / ▶ (run server-side bench) + `releaseName`,
`userId`, `imdbId`, `year` on every sample, and now **per-candidate attempts**
(see Instrumentation below).

**OPP-3 post-bench revision (2026-08-21 evening): the probe is now CONCURRENT
with the full resolve, not sequential.** First implementation probed before
`ProcessNZBImmediatelyWithSource`; the 2×10 ▶ bench regressed prequeue p50 7044→
≈8790ms / mean 7442→≈9386ms (probe mean 864ms per candidate, on the winner's
critical path — 4 probes per iteration). Revision: `Resolve` starts the importer
immediately and runs the probe in a goroutine (`startUsenetPreflight`); a
definitive missing verdict cancels the resolve ctx and returns
`ErrUsenetProbeRejected`; a fast-failing resolve waits up to `preflightVerdictGrace`
(2s) for a pending verdict so rejection isn't masked by a quicker resolve error;
resolve success always wins (proof of availability, e.g. par2 repair). Healthy
happy path now pays **zero** added latency. Tradeoff: a rejected release's
process may have *started* (cancellation is best-effort — importer workers
already outlive caller ctx, see superseded durations below) — "no full download"
became "no full download completes".

**Where things live:** backend branch
`feature/usenet-speed-audit`, **OPP-3 committed at `99ee360b`** (feat(usenet):
concurrent pre-download availability probe (OPP-3)); files in the tree:
handlers/{playback_latency.go, playback_latency_test.go,
playback_latency_admin_test.go, prequeue.go, prequeue_test.go}, services/playback
/{prequeue.go, prequeue_store_test.go, service.go, service_preflight_test.go},
config/settings.go, main.go, this notes file. OPP-3's preflight probe: new
`UsenetPreflightProbeSec` setting (default 5s, 0 disables via backfill→default
is NOT disableable — set it low instead), `playback.ErrUsenetProbeRejected`
(wraps `importer.ErrArticleUnavailable`), `SetUsenetHealthChecker` wired in
main.go; probe runs in `playback.Service.Resolve` concurrent with
`ProcessNZBImmediatelyWithSource`, fail-open semantics (only a definitive
missing-segments verdict rejects; errors/timeouts/deadline fall through to the
full resolve). **
`backend/scripts/latency_bench.sh` is gitignored (`.gitignore: scripts/`) and
must be `git add -f`'d** if it should be committed. Frontend PR is already out
(commit `f7a1a867`, branch `feat/support-racing-resolution-results`, repo
mediastorm-frontend).

**Instrumentation upgraded for OPP-3 (was NOT sufficient before):** samples
now carry `candidates: [{index, releaseName, serviceType, outcome,
 durationMs}]` — every candidate the resolution race tried, with outcome
`adopted | probe_rejected | articles_unavailable | failed | deprioritized |
superseded` and its own wall-clock. Plus **failure samples**: a prequeue that
never reaches ready (all-dead showcase) emits a `complete=false` row with the
candidate list + `notes: "prequeue failed: …"` instead of nothing. Emitted from
`failPrequeue`; consumed pending like the prequeue-only synthesis (mutually
exclusive). Each attempt also logs `[latency] PREQUEUE_CANDIDATE index=…
release=… service=… outcome=… ms=… prequeueId=…`. The ▶ bench CSV now has a
`candidates` column (`idx:release:outcome:ms;…`) and the stdout summary prints a
per-outcome breakdown (count + mean ms) — this is the OPP-3 diff surface:
before, dead releases appeared as `superseded` (cancelled at winner adoption)
or nothing; after, they appear as `probe_rejected` with ms in the seconds.

**Baseline measured (Her, resolve-cold, search-warm, 10× ▶ bench):** all
prequeue-only (`complete=false`, no t2→t4 — winner was the non-HLS SDR release,
LAMA-family): prequeue p50 ≈ **8.8s**, mean ≈ 9.5s, min 7.8s, max 12.3s. NOT
comparable to the 24–37s row in the baseline table below (different provider
state + the winning release varies; the REMUX d3g ran ~14.6s, LAMA ~8s).
**Always diff same-release↔same-release.**

**Auth gotcha (backend bench needed no auth — this is why it was built):** the
admin session cookie is `HttpOnly`; `cmd` copies get the token from
`/admin/api/latency/session-token` (master-only). A 403 on"/admin/api/latency" means the cookie value is invalid/expired/non-master — not
an OPP issue. The ▶ bench sidesteps auth entirely.

**Queued / next session:** 1) OPP-4 (NNTP provider circuit breaker + failover)
— the evening stall/retry chains visible in every run (superseded losers p50
≈12.7s, 17–19s outliers on both OPP-3-era AND pre-OPP-3 binaries) are exactly
its target; 2) the superseded-losers-keep-downloading behavior (importer
workers outlive the caller ctx ~10s+) is worth its own fix — kill in-flight
importer jobs properly on race cancellation; 3) OPP-2's cross-source ordering
nuance is documented in its STATUS block (arrival-order vs global-score order)
— the user may still want to A/B dropping the OPP-2 streaming hypothesis
entirely and measure; 4) add a **Release column + `serviceType` on
prequeue-only samples** (synthesized rows currently show `service=–`), so bench
pastes are ambiguity-free; 5) optional bench knob to **prefer HLS-eligible
releases** when the goal is `complete` (total/ffmpegWarmup) samples; 6) consider
adding `probeMs` (the provider canary, currently log-only) to the bench CSV;
7) new `UsenetPreflightProbeSec` is server-wide (settings JSON) — no admin page
field was added for it (set it in `cache/settings.json` or via the settings
save API).

---

## Instrumentation: click → first frame is measured end to end

A **passive** measurement pipeline already exists. It records the real "time between a
client requesting media and the first playback frame hitting the wire" and splits it
into phases. No frontend changes are needed — playing a title in the app produces a
sample automatically. Files: `backend/handlers/playback_latency.go` (tracker + admin
surface), hooks in `handlers/hls.go`, `handlers/prequeue.go`, `handlers/video.go`.

**Phase timeline (all server wall-clock):**

| ts | event | where |
|----|-------|-------|
| t0 | client `POST /playback/prequeue` (the click) | `PrequeueHandler.Prequeue` + warm/re-click reuse paths |
| t1 | prequeue worker marks entry ready | worker ready block + `AdoptMigration` |
| t2 | HLS session created | existing `HLSSession.StreamStartTime` |
| t3 | first media segment on disk | existing `HLSSession.FirstSegmentTime` |
| t4 | first **segment** response streams to client | `ServeSegment` (segment files only; init.mp4/VTT/playlists excluded) |

Derived phases: `prequeue` (t0→t1, search+resolve+probe), `hlsCreate` (t1→t2),
`ffmpegWarmup` (t2→t3, first segment ready), `serveWait` (t3→t4), `total` (t0→t4).
Emits **exactly one** `[latency] PLAYBACK_LATENCY total=... prequeue=... complete=true ...`
log line per session. Native (non-HLS) clients stop at t1 and log `[latency] PREQUEUE_LATENCY`.

**Per-candidate attempts (OPP-3 instrumentation, 2026-08-21):** every candidate
the resolution race tried rides on the sample as
`candidates: [{index, releaseName, serviceType, outcome, durationMs}]`
(`index` is 1-based feed order; `durationMs` is that attempt's own wall-clock,
not the prequeue). Outcomes: `adopted | probe_rejected | articles_unavailable |
failed | deprioritized | superseded`. Recorded inside `resolveCandidates.process`
(deferred named-return hook, every terminal path), upserted by index; a
fallback winner is flipped `deprioritized → adopted` via
`MarkPrequeueCandidateAdopted`. Each attempt also emits
`[latency] PREQUEUE_CANDIDATE index=… release=… service=… outcome=… ms=…
prequeueId=…`. **Failure samples:** a prequeue that never reaches ready (every
candidate dead/rejected, cancelled, …) additionally emits a `complete=false`
row with candidates attached and `notes: "prequeue failed: <reason>"` from
`failPrequeue` — the all-dead-release path was previously invisible.

Sessions are correlated back to the prequeue two ways: prequeue-created HLS sessions
(HDR/audio transcode) via `LinkHLSSessionPrequeue`; ad-hoc web sessions (`POST /video/hls/start`)
by resolved stream path (`PrequeueStore.FindReadyByStreamPath`). Samples, p50/p95 stats
and the cold-test flush are exposed on the admin UI.

### Admin surface (cookie-auth, same as the rest of the admin SPA)

| endpoint | method | purpose |
|---|---|---|
| `/admin/latency` | GET | live page: sample table + phase chips + flush buttons, auto-refresh |
| `/admin/api/latency` | GET | samples + aggregate stats (`?limit=`), JSON |
| `/admin/api/latency/flush` | POST | cold-test cache flush, **scoped** (see below) |
| `/admin/api/latency/clear` | POST | drop the sample window |

Script: `backend/scripts/latency_view.sh [--flush]` (`FLUSH_SCOPE=resolve|stream` env
selects a scope; `ADMIN_COOKIE` for non-tty auth).

### Flush scopes (isolate a phase — critical for verifying the OPPs)

* **`all`** — full cold: prequeue entries, HLS probe cache, hwaccel detection, live HLS
  sessions (kills ffmpeg), indexer search cache, warm entries, resolved-NZB cache, and
  the NNTP pool (connection drop only — it lazily rebuilds). This is the multi-minute
  worst case (search alone can be ~90s).
* **`resolve`** — resolution cold, **search + stream kept warm**: clears prequeue,
  prewarm, resolved-NZB and the pool. Isolates resolve+parse. Use for OPP-1/OPP-3/OPP-12.
* **`stream`** — transcode cold only: HLS probe cache + hwaccel + sessions. Isolates
  ffmpeg input probing / first-segment warmup. Use for OPP-9/OPP-11.

Buttons for all three are on `/admin/latency`.

### Baseline numbers observed (2026-08-20, dev container, `Her` x265-LAMA)

* `prequeue` (resolve+parse, search warm): **24–37s** ← dominant cost, target of OPP-1/3/12
* `prequeue` including a cold search: **~90–99s** (User's note here: I'm casting doubt on these, don't rely on the 90+s figure too hard)
* `hlsCreate` warm-probe: **~1.6–1.8s**; cold (hwaccel + probe from scratch): **~7s**
* `ffmpegWarmup` probe-cached: **~2.6–2.9s**; cold: **~12.9s**
* `serveWait`: **0ms** (healthy)
* `total` (resolve-cold, stream-warm): **~29–41s**, dominated by `prequeue`

### Dev environment (how the numbers above were produced)

* `backend/dev.Dockerfile` builds `mediastorm-dev:golang-1.26.5` — the pinned golang
  image **plus ffmpeg/ffprobe/python3/wget**. `dev-server.sh` uses it automatically
  (builds once, cached). Without ffmpeg the backend disables transmux: you get
  `ffprobe not configured`, `HLS not enabled`, and no web playback. The dev ffmpeg is
  the Debian 5.1 build (production ships Jellyfin/BtbN 7.x) — fine for dev, no GPU
  (hwaccel falls back to libx264/zscale automatically).
* Serve the web client in dev from the sibling frontend repo:
  `cd ~/off-work/mediastorm-frontend && npm ci && npm run web:export` (builds `dist/` with
  `EXPO_BASE_URL=/watch EXPO_PUBLIC_API_URL=/api`), then
  `STRMR_WEB_APP_DIR=$HOME/off-work/mediastorm-frontend/dist ./dev-server.sh [--watch]`.
  The image bakes the bundle at `/opt/strmr-web`; the backend serves it at `/watch`
  via `handlers/web_app.go` (`ResolveWebAppDir()`: env → `frontend/dist`/`../frontend/dist`/…).
* **Air does NOT hot-reload in this OrbStack/Docker Desktop setup** (fsnotify events
  don't deliver). After editing Go files you must **restart the dev server**
  (Ctrl-C → re-run) to load the new binary, or the running server stays stale.

### Known issues / gotchas discovered during this session

* **nntppool@v1.5.5 upstream race** (`connection_pool.go` `BodyReader`): if the fetch
  errors *exactly when the context deadline fires*, it skips its error return and hands
  back a reader wrapper whose inner reader is nil → `GetYencHeaders()` SIGSEGVs and
  kills the whole server. We contain it at our boundary: `fetchYencHeadersFromPoolReader`
  in `internal/importer/parser.go` nil-checks and converts the panic into a retryable
  error (both header-fetch call sites). Latest nntppool is v1.5.5 — no upstream fix.
  Regression tests: `internal/importer/parser_pool_race_test.go`.
* **NNTP pool must survive `ClearPool`**: `internal/pool/manager.go` now retains the
  provider config and lazily rebuilds on the next `GetPool()` (a "cold pool", not a
  dead one). An earlier flush that permanently nilled the pool broke every NZB
  resolution afterwards ("no providers configured" on all candidates).
* **Frontend `+` → space query bug** (frontend repo, `godver3/mediastorm-frontend`):
  release filenames containing `+` (e.g. `...HDMa5.1.+.Multi...`) break playback
  because the web player inserts a literal `+` into the query string, which Go decodes
  to a space → the WebDAV path no longer matches the stored file → probe fails and the
  stream proxy 405s (hit the `/webdav/` mount root). Fix lives in the frontend (percent-
  encode `+` as `%2B`). Workaround when testing: pick releases without special chars.
* **Admin API auth**: browser admin APIs live under `/admin/api/*` and authenticate via
  the `strmr_admin_session` cookie (`adminUIHandler.RequireMasterAuth`). The `/api/admin/*`
  router is bearer-token only — don't put browser-facing endpoints there.
* **WebDAV local URL** requires `settings.WebDAV` enabled + credentials embedded in the
  base URL (`ConfigureLocalWebDAVAccess`); `GET /webdav/` (collection root) returns 405.
* **`STRMR_WEB_APP_DIR` must be set at container start** (baked at `docker run`), not after.

### Writing/verifying an OPP

1. Restart dev server (see above).
2. `/admin/latency` → flush with the scope from that OPP → play a known-good title.
3. Read the single `PLAYBACK_LATENCY` line / the page row, and diff against the baseline
   table above. For automated runs use `scripts/latency_view.sh --flush`.
4. Add/update a `Verification` test as each OPP already specifies.

---

## Benchmarks: repeatable cold-cache timing per media/release

Goal: measure the SAME title (and the same selected release) on a cold cache,
N times, so OPP landings can be diffed over the span of the work. A baseline
name to record next to any run: media title + exact release + flush scope +
iteration count. The dev baseline table above is the reference point.

* **Sampling:** samples now also carry `releaseName` (the exact selected release),
  backfilled into the latency tracker from the prequeue's selected result. This
  is what makes runs comparable — a different release means a different
  measurement. Set via `NotePrequeueRelease` in `runPrequeueWorker`; validated
  in `TestPlaybackLatencyTrackerRecordsCompleteSample`.
* **Harness:** `backend/scripts/latency_bench.sh` drives the REAL prequeue + HLS
  flow over HTTP (no browser): per iteration it flushes the chosen scope, POSTs
  the prequeue for a title, polls to ready, pulls the HLS session to its first
  media segment (landing t2→t4), reads the server-side sample from
  `/admin/api/latency`, and appends a CSV row.
    - One-click start: `/admin/latency` now has an ⚡ button on every sample
      row that copies a fully-prefilled invocation for that measured release
      (origin, session token, `USER_ID`/`TITLE_ID`/`TITLE_NAME`, and the
      release pinned via `-f`), because the operator picking the row is the
      manual part (they've already validated that release in the app).
    - **Preferred: the ▶ button on the same row runs the benchmark entirely
      server-side** (`POST /admin/api/latency/bench`, master-only). It loops
      flush → real prequeue worker → first HLS segment (through the real
      `ServeSegment` path, whose blocking wait records t3/t4) into the passive
      tracker — no curl, no auth tokens, nothing to paste. Rows land in the
      table as each iteration completes. This sidesteps the cookie/auth
      fragility of the curl harness (admin session cookie is HttpOnly; a 403
      = invalid/expired/non-master session). When the winning release needs no
      transcode (SDR/AAC — no HLS session), the bench synthesizes a
      **prequeue-only row** (complete=false, valid prequeueMs) so the resolve
      phase is never lost to an invisible iteration.
    - Samples carry `imdbId` + `year` (threaded from the prequeue request) so a
      ▶/⚡ re-run scopes the search exactly like the original click (searching
      a title like "Her" with `year=0` rejects everything — the #1 gotcha here;
      the filter logs show `expected title=…, year=0` when it's missing).
    - Requires: `TOKEN` (account/master Bearer token — also drives HLS since
      those routes sit on the protected router), `ADMIN_COOKIE`
      (`strmr_admin_session`), `USER_ID`, `TITLE_ID`, `TITLE_NAME`.
    - Flags: `-n` iterations (default 10), `-s` scope `all|resolve|stream`
      (default `resolve`, isolating resolve+parse = OPP-1/3/12),
      `-f RELEASE` to filter the summary to one release.
    - Output: `backend/cache/latency-bench/<titleId>-<scope>-<date>.csv`
      (gitignored) with columns `ts,iteration,titleName,titleId,releaseName,
      serviceType,serviceProvider,complete,totalMs,prequeueMs,hlsCreateMs,
      ffmpegWarmupMs,serveWaitMs,candidates,notes` plus a mean/p50/p95 +
      per-release breakdown on stdout, and a **per-candidate outcome summary**
      (`probe_rejected x2 (mean=1900ms), adopted x10 (mean=7100ms)` — the
      OPP-3 rejection-latency diff surface). `candidates` is
      `idx:releaseName:outcome:ms;…`, one per race candidate.
    - Only titles that produce an HLS session (HDR/DV/TrueHD/DTS) yield
      `complete=true`; SDR/native titles have no media segment to serve, so the
      script records a **client-measured `prequeueMs`** (t0→t1 wall-clock at the
      harness) row marked `complete=no` / "prequeueMs client-measured" — the
      resolve phase is still comparable (that's the OPP-1/2/3/12 metric), just
      not t2→t4. Rows are only ever attributed by their `prequeueId`; the
      admin flush and sample reads verify the HTTP status so a bad/expired
      `ADMIN_COOKIE` fails loudly (it used to silently no-op the flush and
      crash the recorder on the empty 401).
* **Workflow:** before starting an OPP, run a cold round and save the CSV as the
  before-baseline (e.g. `cp` to a non-ephemeral name); after the OPP lands and
  the dev server is restarted, run the same command again and diff the CSV
  (`-f` same release, same scope). Note the release can legitimately change
  when candidate selection changes (OPP-1 first-success-wins).

### Reference baseline — 2026-08-21 (`Her`, resolve-cold / search-warm, ▶ bench)

Recorded after OPP-1 + OPP-2 had both landed and the crash/warm-cache fixes
were in (`09c96276`), dev container, **2×10 ▶ bench iterations**
(`scope=resolve`, 10 iterations per run, two runs back-to-back). All rows are
prequeue-only (`complete=false`, SDR winner — same shape as the OPP-1
measurement; release varies via first-success-wins), so these are the
`prequeue` phase numbers the OPP-1/2/3/12 work targets.

| run | n | min | p50 | mean | max |
|-----|---|----:|----:|-----:|----:|
| run 1 (18:02) | 10 | 4922ms | 7160ms | 7206ms | 9915ms |
| run 2 (18:10) | 10 | 4663ms | 7044ms | 7442ms | 11550ms |
| **combined** | **20** | **4663ms** | **7044ms** | **7324ms** | **11550ms** |

Raw (combined, sorted ms): `4663, 4922, 5043, 5090, 5602, 6719, 6834, 6868,
6885, 7012, 7075, 7487, 7565, 7880, 8033, 8325, 9128, 9880, 9915, 11550`.

Compared with the OPP-1 session's own 10× ▶ baseline (p50 ≈ 8.8s, mean ≈
9.5s, min 7.8s, max 12.3s), the post-OPP-1+OPP-2 median/mean is ~7.0–7.3s.
Same caveats as always: provider state and the *winning release* vary between
runs (benchmarks recorded OPP-1's 24–37s row as *not* comparable; the d3g
REMUX resolves slower than the LAMA-family SDR that keeps winning here), so
this is a same-setup, same-title trend line, not a controlled A/B. The raw
runs above are the source of truth if a precise diff is ever needed again.

---

## OPP-1: Race top-K candidates instead of serial resolution

> **STATUS (2026-08-20): DONE.** `resolveCandidates()` races the ranked list with a
> width-4 worker pool (`racePrequeueResolutions`, prequeue analog of
> `checkFastestMode`); first fully validated candidate wins, losers abort on a
> cancelled race ctx, deferred debrid preflight kept via a once-only gate, bad-stream
> marking preserved. Aborted (superseded) losers log as "superseded by winner", not
> "Failed to resolve". OPP-1 verification tests + a race-detector pass in place.
> A still-open **release-selection nuance**: first-success-wins can pick a slightly
> lower-ranked release that happens to resolve fast over a slow-but-healthy #1.

**Problem.** The prequeue worker resolves candidates one at a time, in rank order,
fully downloading and probing each before touching the next. If the #1-ranked
release is dead or slow (missing articles → full download attempt before failure),
candidates #2–#N sit idle for minutes.

**Location.** `backend/handlers/prequeue.go:1905–2127` (the
`for i := range allResults { Resolve(); ProbeVideoFull(); ... }` loop);
`backend/services/playback/service.go:188` (`Resolve`).

**Idea.** Replace the serial loop with a bounded worker pool that resolves the top-K
candidates (across both usenet and debrid) concurrently and adopts the first success.
The debrid path already has a reference implementation: `checkFastestMode` in
`backend/services/debrid/multiprovider.go:117–173` (one goroutine per provider,
first `IsCached` wins, rest cancelled). Cancel/cleanup semantics and the
`IsArticleUnavailable` → bad-streams marking at `prequeue.go:1953–1968` must be
preserved.

**Expected impact.** High — removes the worst-case "one dead release stalls the
whole queue" scenario; bounds resolution to the fastest healthy candidate.

**Verification.** Add a test with a slow/failing top candidate and a fast second
candidate; assert the fast one is adopted and elapsed wall-clock is not the sum of
both.


---

## OPP-1 API contract: progress during concurrent resolution (frontend OTA skew)

> Backend ships via admin action; the frontend is OTA. Keep the prequeue status API
> **additive and skew-tolerant**.
>
> * `PrequeueStatusResponse`/`PrequeueEntry` gained `progressCurrentMin` /
>   `progressCurrentMax` (1-based in-flight candidate window) + `progressCurrent`
>   now equals the window max. All `omitempty` → older clients see the unchanged JSON.
> * Frontend (`features/details/prequeue-progress.ts`) must read them with a fallback:
>   `const min = status.progressCurrentMin ?? status.progressCurrent ?? 0` (same for
>   max), render `X–Y of Z` when `min < max`, single `N of Z` otherwise. Old-backend +
>   new-frontend degrades to today's copy; old-frontend + new-backend still shows the
>   window max (truthful, less precise). No new stage strings were introduced.
> * Pinned by `TestPrequeueProgressWindowFieldsAreAdditiveAndOmitEmpty`
>   (services/playback/prequeue_store_test.go) — the fields ride ToResponse and are
>   absent from JSON when unset.
> * Progress numerics are now owned by the race owner; worker updates carry only
>   stage + release detail (`updatePrequeueStageDetail`), so "stream 4 of 10" can no
>   longer be the serial left-over.

---

## OPP-2: Stream search results per source (don't wait for both usenet + debrid)

> **STATUS (2026-08-21): DONE.** The prequeue now resolves candidates as each
> search source streams in instead of waiting for both (`SearchWithScoringSplit`
> emits each source's filtered+ranked **passed** candidates the moment that
> source completes — a background drain, so the call returns channels
> immediately; usenet typically lands while debrid scrapers are still gated).
> The resolution race now consumes a `prequeueCandidateSource`
> (`resolveCandidates(src, …)`): a fixed `sliceCandidateSource` (tests/derived
> paths) and a `streamCandidateSource` the feeder publishes into. Per-source
> bad-stream marking, episode annotation, a `maxCandidates` (50) cap, and the
> deferred debrid `PrepareTorrentCandidates` preflight all moved to the feeder;
> the shared "raw" search-cache write is preserved (after both sources settle,
> only when none failed and none was incomplete). Search-chunk probes: usenet
> resolution *starts and completes* before debrid exists
> (`TestResolveCandidatesStreamsUsenetBeforeDebrid`, handler-level) and the
> split emits usenet while debrid is still gated
> (`TestSearchWithScoringSplitEmitsUsenetBeforeSlowDebrid`, service-level).
>
> Tradeoffs to keep in mind:
>   - Cross-source order is now source-arrival order (usenet-before-debrid for a
>     typical usenet-first install), not a global score sort — first-validated
>     wins can pick a usenet candidate that a higher-scored (but still
>     searching) debrid candidate might have beaten. That is the intended
>     OPP-2 tradeoff for usenet-prioritized installs.
>   - Debris search is cancelled (via the feed context) once a winner is
>     adopted, rather than left to finish.
>   - Prequeue-only latency samples (`complete=false`) are unaffected; the OPP-2
>     win is entirely in the `prequeue` phase, which the `resolve`-scope bench
>     isolates.
>
> **Bench crash find (2026-08-21, fixed in the follow-up commit):** the first
> ▶-bench run died with `panic: close of closed channel` in
> `streamCandidateSource.Stop`, and the iteration before it showed a warm-cache
> `usenet` emit of 0 passed candidates. Two bugs: (1) `streamCandidateSource.Close`
> closed `s.done` without setting `stopped`, so the worker's unconditional
> `Stop()` after an exhausted race double-closed it — `Close` now sets `stopped`,
> making both teardown paths idempotent; (2) the split's **cache-hit path** emitted
> the raw-cache partitions without re-scoring them (`Scored` was nil), so on a
> warm cache the prequeue had nothing to resolve; the cache-hit path now runs
> `scoreSourceCandidates` per partition. Regression tests:
> `TestStreamCandidateSourceCloseAndStopIdempotent` and
> `TestSearchWithScoringSplitCacheHitStillEmitsScoredUsenet`.

**Problem.** The play path waits for *all* search sources to finish before resolving
anything. `searchRawResults` closes its results channel only after `wg.Wait()` over
both usenet and debrid, so a usenet-prioritized install still waits on every slow
debrid scraper.

**Location.** `backend/services/indexer/service.go:2030–2033` (the `wg.Wait()` /
`close(resultsChan)` block); `backend/services/indexer/service.go:2209`
(`SearchSplit` already exists but is unused by prequeue).

**Idea.** Wire the prequeue worker to use `SearchSplit` (or otherwise consume
results as each source streams in) so the top usenet candidates can begin resolving
while debrid scrapers are still in flight. Preserve ranking/merge semantics and the
search-cache write behavior.

**Expected impact.** High — removes debrid-scraper tail latency from the usenet
critical path.

**Verification.** Test with a deliberately slow debrid scraper and a fast usenet
indexer; assert usenet resolution starts (and completes) before debrid returns.

---

## OPP-3: Pre-download Usenet availability probe (reject dead releases cheaply)

> **STATUS (2026-08-21): DONE — concurrent revision.** `playback.Service.Resolve`
> runs a cheap availability probe **parallel to** `ProcessNZBImmediatelyWithSource`
> (started first, goroutine-backed via `startUsenetPreflight`), reusing the
> existing health-check machinery verbatim (`usenet.Service.CheckHealthWithNZB`
> → `sampleSegmentsForHealth` first/middle/last sampling + concurrent BODY
> checks across enabled providers — no new NNTP code). A definitive
> missing-segments verdict cancels the resolve ctx and returns
> `playback.ErrUsenetProbeRejected` (wraps `importer.ErrArticleUnavailable`), so
> the existing prequeue bad-stream marking (reason
> `prequeue:usenet-articles-unavailable`) fires unchanged and the instrumentation
> can tell probe rejection apart from full-download failure. A resolve that
> fails before the verdict lands waits up to `preflightVerdictGrace` (2s) for it
> (`preflightProbeRejectedAfter`), so a fast-decaying resolve error never masks
> a rejection; resolve *success* always wins (fully downloaded = available, e.g.
> par2 repair), and the probe ctx is cancelled with the resolve outcome.
> **Fail-open by design:** no checker wired / checker error / no providers /
> budget timeout are all *inconclusive* → resolve outcome stands; only `Healthy
> == false` rejects. Budget = `Streaming.UsenetPreflightProbeSec` (default 5s;
> 0 = backfilled to 5s — lower it if a provider is slow). Skips: resolved-NZB
> cache hits, debrid candidates, external-engine candidates (probe is
> direct-provider-only).
>
> **Why concurrent (bench data):** the first implementation probed *before* the
> full resolve and paid on the happy path — 2×10 ▶ bench, same title/scope:
> prequeue p50 7044→≈8790ms, mean 7442→≈9386ms, probe mean 864ms per candidate
> (80 probes), all on the winner's critical path plus provider contention from 4
> concurrent probes per iteration. The concurrent revision removes the tax for
> healthy releases; the price is that a rejected release's download may have
> started (importer cancellation is best-effort — superseded losers already run
> ~10s+ past adoption, see instrumentation) — "no full download" became "no
> full download completes".
>
> **Provider-vs-code control (2026-08-21, definitive):** the remaining ~2s over
> the 18:10 baseline survives on a **pre-OPP-3 binary**. The working tree was
> stashed and the server rebuilt at `5e5da737`; a 10× ▶ bench at 19:20 (same
> title/scope, ZERO availability-probe code in the binary) measured prequeue p50
> ≈ 9218ms / mean ≈ 10701ms / max 18824ms — at or above the OPP-3-era runs
> (18:45–19:09: p50 8432–9518, means 9285–10325). Also the probe itself acts as
> a provider canary: `probeMs` mean rose 864ms (18:45) → 1575ms (19:07).
> Conclusion: the evening uptick vs the 18:02/18:10 reference baseline is
> provider-side (time-of-day congestion), not OPP-3. Bench discipline note:
> same-day, same-cadence runs; treat the 18:10 numbers as the best provider
> state, not a code guarantee.
>
> Verification: `services/playback/service_preflight_test.go`
> (`TestResolveProbeMissingRejectsPromptly` — missing verdict wins over the
> importer error via the grace path, prompt, both error predicates hold;
> `TestResolveProbeVerdictWaitsOutFastResolveError` — gated checker proves a
> pending verdict is waited out, not masked;
> healthy/error/timeout/no-checker → fall through; budget deadline applied) +
> handler-level `TestResolveCandidatesProbeRejectionMarksBadStream` (bad-stream
> entry created, next candidate adopted, tracker records `probe_rejected`
> quicker than `adopted`) + race-detector pass. Instrumentation (candidate
> attempts + failure samples, see handoff) was added in the same session because
> the old surface could not measure the rejection path.

**Problem.** Usenet has no "instant availability" signal. The first time a release
is known to be incomplete/dead is after the article fetch (full download + parse),
which the prequeue then reacts to only *after* the cost is paid.

**Location.** `backend/services/playback/service.go:235–280` (fetch NZB →
`ProcessNZBImmediatelyWithSource`); failure handling at
`backend/handlers/prequeue.go:1953–1968`; health-check plumbing at
`backend/services/usenet/service.go:63` (`CheckHealth`) and `:472–491` (segment
sampling).

**Idea.** Add a cheap pre-flight availability check before the full download — an
NNTP `STAT` / header-only pass over a small sample of segments (first/middle/last),
reusing the existing segment-sampling logic, so dead/incomplete releases are
rejected in seconds instead of minutes. This is the Usenet analog of debrid's
`CheckInstantAvailability` (`backend/services/debrid/health.go:235`). Result can
also feed candidate ranking/`added`-style badges.

**Expected impact.** High — converts the most expensive per-candidate failure mode
into a cheap one; directly shortens the OPP-1 racing loop.

**Verification.** Test with a known-incomplete release; assert it is rejected via the
cheap probe (no full download) and the bad-stream marking still fires.

---

## OPP-4: NNTP provider circuit breaker + failover

**Problem.** A slow or failing NNTP provider blocks and aborts the whole health
check (and therefore availability), with no breaker/backoff. The existing
`providerbreaker` is HTTP-indexer-only.

**Location.** `backend/services/usenet/service.go:668–672` (returns on first
provider error); `:683–855` (`checkSegmentsOnProvider` aborts all workers on any
connection error); `backend/internal/providerbreaker/breaker.go` (HTTP-only).

**Idea.** Port the debrid resolution-circuit pattern
(`backend/services/debrid/resolution_circuit.go`: per-provider timeout, open/half-open
state, exponential cooldown, single recovery probe) to the NNTP layer. A failing
provider should fail fast (~12s) and be skipped for a cooldown window, letting a
second provider serve the content instead of serializing/failing the whole check.

**Expected impact.** High — bounds worst-case resolution during outages and makes
multi-provider setups resilient.

**Verification.** Test with a provider that times out; assert the check fails fast,
falls over to the next provider, and the first provider is short-circuited on the
immediately following call.

---

## OPP-5: True progressive RAR indexing (surface first playable file early)

**Problem.** The "progressive" RAR analysis actually indexes *every* volume before
the first file's metadata is written. The first playable byte is gated on the full
volume set being indexed, which is the largest fixed cost of the built-in engine.

**Location.** `backend/internal/importer/rar_processor.go:215–251`
(`DiscoverVolumesFS` → `IndexVolumesParallel` → `AggregateFiles`, all before the
`callback` loop); the `firstVideoFound` short-circuit at
`backend/internal/importer/processor.go:451–529` (runs only after full indexing).

**Idea.** Parse the first volume's local file headers, materialize the first playable
content file's segment map, and return it immediately, indexing tail volumes lazily
(and cancelling once the first playable file is found). Mind the split-file/
multi-volume boundary case where the target file spans volumes.

**Expected impact.** High — directly reduces time-to-first-byte for large multi-volume
releases.

**Verification.** Test with a many-volume RAR; assert the WebDAV path is returned
before all volumes are indexed, and that streaming the first file still works.

---

## OPP-6: Raise yEnc header-fetch concurrency (parallel file parsers)

**Problem.** Per-volume yEnc header fetching (first + last segment per file) is
throttled to 4 concurrent file parsers, far below the RAR worker pool (40). A
100-part RAR ≈ 200 NNTP round-trips at width 4 *before* RAR indexing begins.

**Location.** `backend/internal/importer/parser.go:27`
(`maxConcurrentNZBFileParsers = 4`); `:265` (`runBoundedFileParsers`); `:374` and
`:414` (segment 0 then last segment per file).

**Idea.** Raise the concurrency bound (or make it configurable) and/or fetch the
first and last segment headers in parallel per file. Verify the underlying fetch
path tolerates the higher concurrency without provider connection exhaustion.

**Expected impact.** High — collapses the serialized round-trip count on the import
critical path.

**Verification.** Benchmark import of a large multi-file NZB before/after; assert
wall-clock drops and file sizing/metadata is unchanged.

---

## OPP-7: Reuse NNTP connections (idle timeout + pooled health checks) and cache DNS

**Problem.** The streaming path's first byte is gated on a cold connection handshake;
the pool keeps only 2 warm connections and idles them out at 10s, so any gap >10s
between click and stream means dial+TLS+AUTH inline. The health check bypasses the
pool entirely and opens fresh connections per worker. DNS is cached only for HTTP,
never NNTP.

**Location.** `backend/internal/pool/manager.go:72–78` (`MinConnections: 2`);
`backend/config/adapter.go:156–160` (10s idle / 900s TTL);
`backend/services/usenet/service.go:574–578` (pool bypass, with a TODO to re-enable);
`backend/services/usenet/nntp.go:28–66` (bare `net.Dialer`, no DNS cache).

**Idea.** Raise the idle timeout and warm-connection count; re-enable the pool for
health checks once the upstream `nntppool` `Stat` bug is addressed (or use `BODY`
via the pool); feed the `dnscache` dialer into both `newNNTPClient` and the pool
construction so NNTP hosts skip per-connection DNS lookups.

**Expected impact.** High for back-to-back probe→stream flows; removes the cold
handshake and DNS RTT from time-to-first-byte.

**Verification.** Confirm the pool's `Stat` bug reproduction, then measure
time-to-first-byte with a warm pool vs cold; assert DNS is resolved via the cache.

---

## OPP-8: External engine — exploit direct-unpack streaming + fast-then-backoff polling

**Problem.** SABnzbd/nzbget installs wait for full download + extract completion,
polled at a fixed 2s interval. SABnzbd's direct-unpack streaming mode can expose a
partially-downloaded stream far earlier but is never exploited.

**Location.** `backend/services/usenetengine/sab.go:348–361` (maps
`streaming`/`downloading`/`extracting` → `StatusProcessing`); `:135–149` (two
sequential HTTP calls per poll); `backend/handlers/prequeue.go:2578–2615`
(`waitForPlaybackQueue`, fixed 2s ticker); `backend/services/playback/service.go:657`
and `:802–866` (only returns a stream URL on `StatusCompleted`).

**Idea.** Surface a playable stream as soon as SABnzbd reports a streaming/
direct-unpack state; replace the fixed 2s poll with fast-then-backoff (or a
push/long-poll) so completion granularity isn't 2s; optionally collapse the two
status calls into one parallel/combined request.

**Expected impact.** High for external-engine installs — removes the entire
download+verify tail plus poll granularity from the perceived delay.

**Verification.** Against a SABnzbd test double, assert a stream URL is returned at
`streaming`/`downloading` state, and that the poll interval backs off.

---

## OPP-9: Pre-warm ffmpeg input + probe on the prequeue path

**Problem.** ffmpeg's input is only opened on click; a fresh ffmpeg process + fresh
usenet connection is spawned, then it waits on `-probesize/-analyzeduration` before
producing segment 0. The prequeue warms the ffprobe cache but not ffmpeg or its
upstream connection.

**Location.** `backend/handlers/hls.go:3806` (`startTranscoding` →
`exec.CommandContext`); `:3128–3129` (probe limits); `:3008–3024` (usenet→WebDAV
connection per session); `backend/handlers/video.go:6186` (`CacheProbe`).

**Idea.** Pre-open/prime the resolved usenet stream (and its input probing) at
prequeue time so the click only has to spawn ffmpeg against an already-warm input.
Consider reusing the resolved `probeAllMetadata` result to skip redundant input
probing.

**Expected impact.** High — shifts the ffmpeg input-probing stall off the click
path.

**Verification.** Measure `StartHLSSession` latency with a warm vs cold input; assert
the prewarm path removes the input-probe stall.

---

## OPP-10: Use input seeking for short resumes (avoid reading from byte 0)

**Problem.** Resume offsets under 30s use output seeking (`-ss` after `-i`), which
forces ffmpeg to decode/download from byte 0 up to the resume point before segment 0.

**Location.** `backend/handlers/hls.go:3137–3139` (comment noting small seeks use
output seeking); `:3211–3222` (`useOutputSeeking`).

**Idea.** Use accurate input seeking (HTTP Range based) for short resumes too, so a
25s resume doesn't pull ~60MB of articles before the first frame. Verify accuracy
trade-offs and that keyframe position handling stays correct.

**Expected impact.** High for resume/continue-watching flows (very common path).

**Verification.** Resume a stream at <30s and assert the initial article fetch starts
near the resume point, not at byte 0.

---

## OPP-11: Make hwaccel detection run at startup/prewarm, not first play

**Problem.** The first-ever transcode pays full hardware-accel detection inline:
`ffmpeg -encoders`, `ffmpeg -filters`, tone-map probes (10s-timeout subprocesses),
and a null test-encode.

**Location.** `backend/handlers/hwaccel.go:83` (`hwAccelCaps`), `:215`
(`detectHWAccel`); cached afterward at `hwaccel.go:97`.

**Idea.** Run detection at server startup or prewarm so the first play never blocks
on subprocess probing. Keep the existing cache as-is for subsequent requests.

**Expected impact.** High for first-play-after-boot; medium overall.

**Verification.** Assert `hwAccelCaps` returns a cached result on the first play
after startup with detection pre-populated.

---

## OPP-12: Background NZB preflight + prewarm the resolved-NZB cache

**Problem.** Usenet resolution work (NZB fetch + parse + index) happens entirely on
the click path. Debrid already has background preflight/prewarm patterns Usenet lacks.

**Location.** `backend/services/debrid/torrent_preflight.go:100–140`
(`PrepareTorrentCandidates`, the pattern to mirror);
`backend/handlers/prequeue.go:1931–1943` (deferred preflight hook);
`backend/services/playback/service.go:219` (`FindResolvedNZBByDownloadURL`, the cache
to prewarm); `backend/services/prewarm/service.go:928–986` (`RefreshURLs`, the
refresher pattern).

**Idea.** Prefetch/parse NZB headers in a small worker pool and cache completion
metadata, deferred until a usenet candidate is eligible (as debrid already does).
Add a refresher that re-resolves/validates recently-watched titles in the background
so the next selection is a warm cache hit.

**Expected impact.** High — turns a serial download+parse into prefetched, parallel
work that reuses cached data.

**Verification.** Assert a prewarmed title resolves from cache without a fresh NZB
fetch, and that preflight does not delay higher-ranked candidates.
