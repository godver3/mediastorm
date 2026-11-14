# Report-aware source hints

From `backend`, run:

```sh
go test ./services/sourceavailability ./services/badstreams ./services/indexer ./handlers
go test -race ./services/sourceavailability ./services/badstreams -run 'Availability.*(Report|Concurrent|Prepared|Evidence|Legacy|Temporary|Track|Episode|Language|Private)|ReportRevision' -count=1
```

`reports_test.go` exercises the real report registry and cached observations.
It checks five language intents, title overrides, private and shared rules,
provider/device boundaries, undo, temporary expiry, alternate releases, legacy
blacklists, restart, preparation races, redaction and bounded evidence. A reported
track remains unverified until a media probe identifies a usable track; search
language hints cannot prove every track broken. Episode reports do not qualify a
whole-series overview. Exact episode badges remain a separate feature.

The registry's context revision invalidates the home manifest when applicable
rules change or expire. Search evidence stays under the original preference and
permission scope and is re-evaluated at presentation time. Reporting and undo do
not reset budgets, download an NZB or initiate provider work from an overview.
Successful movie preparations without full search evidence become unknown when
applicable rules change and can be checked again by the existing bounded worker.
An episode preparation no longer advertises a general series source.

At most 20 projected candidates are stored per observation. Only release title,
source identity, provider name and language hints are retained. Download URLs,
GUIDs, stream paths and arbitrary provider attributes are excluded. Partial,
truncated, unknown-language and unverified-track evidence cannot prove absence.
Old aggregate records and unsupported evidence versions become unknown and are
eligible for the normal quota-limited refresh. The feature remains off by default.

`TestAvailabilityReportsRefreshMetadataAndHomeManifest` connects real metadata,
home-manifest, reporting and undo handlers to an isolated availability worker.
The provider search is deterministic and returns synthetic candidates. It proves
that shared French rules preserve English and that one healthy alternative keeps
the source hint. There is no decoder, provider account or private playlist.

Set `MEDIASTORM_BROWSER_PYTHON` to a Python interpreter with Playwright and
`MEDIASTORM_CHROMIUM` to a Chromium executable, then run:

```sh
go test ./handlers -run '^TestAvailabilityReports' -count=1
```

`MEDIASTORM_BROWSER_ARTIFACTS` optionally selects the screenshot/result directory.
The browser checks the actual HTTP responses through a minimal synthetic test
page. Its layout is a backend fixture, not a screenshot of the native catalogue.
It does not establish device playback or new native grey-out behavior.

An isolated preview can be started with an explicit private test address:

```sh
MEDIASTORM_SCOPED_AVAILABILITY_PREVIEW_ADDR=127.0.0.1:18903 \
  go test ./handlers -run '^TestAvailabilityReportsPreview$' -count=1 -v -timeout=0
```

The fixture routes are compiled only in the test binary and never registered by
the application. Do not point these tests at a normal server.
