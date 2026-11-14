# Isolated application browser suite

`TestFullApplicationBrowser` drives the actual web client and embedded playback
page against a separately started application with PostgreSQL. It complements
the synthetic HTTP component fixtures and native unit tests. Nothing in this
suite starts a production deployment or reads an existing playlist/media folder.

Use a fresh database and a loopback-only server with `-demo`, PIN enforcement,
an independent cache directory and a complete static web-client bundle. Put a
generated H.264/AAC English-tagged test pattern of 90 seconds at
`<isolated-cache>/demo-media/tt0063350.mp4`. The demo catalogue title is only a
metadata label; this fixture does not contain the actual film.

A private mode-0600 bootstrap JSON file supplies the isolated server's account
and password. Generate a fresh synthetic fixture before the test:

```sh
python3 testdata/setup_family_application.py \
  --base-url "$MEDIASTORM_FULL_APPLICATION_BASE_URL" \
  --bootstrap-credentials-file "$PRIVATE_BOOTSTRAP_FILE" \
  --output "$MEDIASTORM_FULL_APPLICATION_CREDENTIALS"

go test ./handlers -run '^TestFullApplicationBrowser$' -count=1
```

Also set `MEDIASTORM_BROWSER_PYTHON`, `MEDIASTORM_CHROMIUM` and optionally
`MEDIASTORM_BROWSER_ARTIFACTS`. Relative paths above assume the handlers directory
for setup, and the backend directory for `go test`. The credential file is not a
repository asset. The setup refuses an existing output file, requires loopback
and checks `demoMode` before creating accounts. Failed setup removes only the
accounts it just created. Default account profiles remain in the isolated data.

The guard suite runs in normal and optimized Python so protection never relies
on disabled assertions. The browser validates its synthetic marker, profile names and ownership, and
requires demo mode before clearing only the generated movie's progress. It
checks foreign-profile denial, PIN enforcement and separate progress; signs in
through the actual UI, selects a profile and enters its PIN; opens a real card
and presses Play; requires decoded video frames; cancels the actual report
menu without changing source; uses pause, seek and resume controls; and reads
persisted progress through an independent authenticated request. Desktop/mobile
screenshots and a credential-free JSON result are produced. A single measured
click-to-frame value is not a p95 or real-provider performance result.

Without provider keys this demo can show detail-metadata and Netflix chart
errors while playing the generated file. Those specific requests are recorded
as known limitations; other server errors and page exceptions fail the suite.
The client build used must be documented separately. Native Compose rendering,
physical focus/remote input, actual providers, spoken language and family age
policy remain separate acceptance work. Optional external or hardware suites
must not be represented as passed when skipped.

The combined acceptance branch includes the existing backend feature stacks;
merge their component PRs before treating this test package as a release.

`preview.html`, the four screenshots and `last-reviewed-results.json` capture
one reviewed run of this full application suite. The private test application
uses an isolated database; the evidence viewer exposes only these synthetic
artifacts. The prepared demo click-to-frame sample is not a cold-start or p95
measurement. The regression also exposed a foreign-profile prewarm ID reuse;
the requesting profile now resolves through its own indexed preparation before
source validation.
