# MLB detail fixture

`mlb-summary-final.json` is a reduced factual response from ESPN's MLB summary endpoint for event 401816843, retrieved September 8, 2026 from the isolated development container:

https://site.api.espn.com/apis/site/v2/sports/baseball/mlb/summary?event=401816843

It retains identity, final status, inning scores, team statistics and four textual play records used by the parser tests. It does not establish live situation availability. ESPN's consumer endpoint is undocumented; missing and unknown fields must be handled defensively. No third-party project code was copied.

`mlb-summary-live.json` is a reduced live ESPN response for event 401816854, captured September 8 at approximately 22:36 UTC. It retains the zero-count situation and player IDs/names needed for lookup tests. `situationOccupiedSample` is a later actual situation from that same event with a runner on second. It is test-only provenance and not an upstream response field. Unknown base fields do not establish an empty-base state.

## Racing captures
`f1-racing-captured.json` is the ESPN F1 scoreboard retrieved September 9, 2026 UTC: https://site.api.espn.com/apis/site/v2/sports/racing/f1/scoreboard . Parent 600057442 contains separate FP1/FP2/FP3/Qual/Race sessions. Athlete IDs are supplied by competitor.id, not the nested athlete. Driver order is not a classification rank.

`f1-race-statistics-captured.json` was retrieved from https://sports.core.api.espn.com/v2/sports/racing/leagues/f1/events/600057442/competitions/401839102/competitors/5829/statistics/0 . It supplies explicit place, lap counts and display timing values. Both captures represent completed sessions; live timing cadence is unverified.

## ASO cycling captures
`aso-{tour,vuelta,tour-femmes}-stages.json` and `aso-{tour,vuelta,tour-femmes}-results.json` are reduced official race-center JSON captured September 9, 2026 UTC. Hosts: `racecenter.letour.fr`, `racecenter.lavuelta.es`, `racecenter.letourfemmes.fr`. Schedules use `/api/stage-2026`; results use `/api/rankingType-2026-21`, `-16`, and `-9` respectively. Fixtures retain all arrival `ite`/`itg` ranking rows and referenced checkpoint/rider/team identifiers and names. Marketing text, biographies, images, birth dates and unrelated classifications are omitted. These samples validate available finish classifications, not live/final status. Vuelta includes an unranked competitor; Tour/Femmes stage and GC tables must remain independently scoped.

## NHL player capture

`nhl-player-captured.json` retains the ESPN historical summary for event 401777460 (Edmonton at Florida, 2025-06-18 UTC), retrieved 2026-09-11. Endpoint: `https://site.api.espn.com/apis/site/v2/sports/hockey/nhl/summary?event=401777460`. Used for player/goalie column alignment and category tests. In this response `SOG` means shootout goals; ordinary shots use `S`. Season totals are excluded.

## Pregame matchup captures (September 15, 2026)

- `pregame-mlb.json`: https://site.api.espn.com/apis/site/v2/sports/baseball/mlb/summary?event=401816943
- `pregame-nba.json`: https://site.api.espn.com/apis/site/v2/sports/basketball/nba/summary?event=401902644
- `pregame-eng.1.json`: https://site.api.espn.com/apis/site/v2/sports/soccer/eng.1/summary?event=401879275

Reduced official summaries retain factual team identities, season/records, selected season statistics, recent results, leaders, probable starters and completed head-to-head results. Standings retain only the matchup's team rows; unrelated media, articles, odds, links and images are removed. The NBA sample is preseason with 0-0 records and must not imply meaningful zero season averages. Minute-only ESPN dates must remain parseable. These undocumented response shapes are optional; no additional requests are issued to populate the pregame preview.
