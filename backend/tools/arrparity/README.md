# arrparity

`arrparity` measures how closely mediastorm's release evaluation
(`internal/arr`) matches a real Sonarr or Radarr. mediastorm does not reuse
any Sonarr/Radarr code (both are GPL-3.0); the parser and custom format
engine are written from their documented behaviour, and this tool checks the
result against a running instance used as an oracle.

The committed fixtures in `internal/arr/testdata/parity/` record what Sonarr
4.0.20 and Radarr 6.4.4 reported through `/api/v3/parse` for every title in
`*-synthetic.txt` and `*-handpicked.txt`, with all custom formats of the
bundled TRaSH snapshot installed. `TestParityWithRecordedOracle` replays them
on every `go test` run and fails when a field drops below its threshold, so
no oracle is needed for normal development.

## Commands

```
arrparity capture      -app sonarr -url http://127.0.0.1:8989 -key KEY -titles a.txt,b.txt -out fixture.json.gz [-trash-commit SHA] [-workers 8]
arrparity push-formats -app sonarr -url http://127.0.0.1:8989 -key KEY -trash <Guides>/docs/json
arrparity report       -fixture fixture.json.gz [-trash <Guides>/docs/json] [-show 30] [-field languages] [-tsv]
arrparity parse        -app radarr "Some.Movie.2024.German.DL.1080p.BluRay.x264-GRP"
arrparity bundle       -trash <Guides>/docs/json -commit SHA -out internal/trash/bundle/trash-guides.tar.gz
```

- `push-formats` creates or updates every TRaSH custom format on the
  instance, so `capture` records matched custom formats too.
- `capture` asks the instance to parse every title and writes a fixture
  (gzip-compressed when the name ends in `.gz`).
- `report` re-parses the recorded titles with mediastorm and prints match
  rates per field and the mismatches. `-tsv` is convenient for grouping.
- `parse` prints mediastorm's parse of the given titles.
- `bundle` packs the parts of TRaSH-Guides `docs/json` that mediastorm uses
  (custom formats, CF groups, quality profiles, quality sizes, conflicts)
  into the archive embedded by `internal/trash` as the offline fallback.

## Refreshing after a TRaSH update

The fixtures and the bundle must come from the same TRaSH commit; the parity
test refuses to compare them otherwise.

1. Download TRaSH-Guides at the new commit
   (`https://codeload.github.com/TRaSH-Guides/Guides/tar.gz/<sha>`).
2. Rebuild the bundle:
   `go run ./tools/arrparity bundle -trash <Guides>/docs/json -commit <sha> -out internal/trash/bundle/trash-guides.tar.gz`
3. Start a disposable Sonarr v4 and Radarr (official release binaries are
   enough: `Sonarr.exe -nobrowser -data=<empty dir>`; read the API key from
   `<data>/config.xml`). Use instances that hold no real library.
4. For each app: `push-formats`, then `capture` with the two title lists of
   that app and `-trash-commit <sha>`, writing
   `internal/arr/testdata/parity/<app>.json.gz`.
5. `go test ./internal/arr -run Parity -v` prints the new rates. Fix the
   parser or custom format engine where it regressed; adjust a threshold in
   `parity_test.go` only with a written reason.

Also re-run step 4 after upgrading the reference Sonarr/Radarr versions.

## Title lists

`*-synthetic.txt` comes from `gen_corpus.js`, which builds deterministic,
realistic titles (with an emphasis on German releases) from the TRaSH
release group lists:

```
node tools/arrparity/gen_corpus.js <Guides>/docs/json internal/arr/testdata/parity
```

`*-handpicked.txt` is written by hand: common naming styles and edge cases
that once disagreed with the oracle.

`extract_corpus.js` pulls release-title inputs out of the Sonarr/Radarr
parser tests and ptt-go for a much larger local check. Those titles come from
GPL-licensed test sources: use them only locally (`capture` into a scratch
fixture and `report` it) and never commit the extracted lists or fixtures
built from them.
