# Quality presets

A quality preset ranks and filters search results the way Sonarr or Radarr
would: a quality profile (allowed qualities in order, minimum custom format
score, size limits) plus custom formats with scores. Presets come from
TRaSH-Guides, from a Sonarr/Radarr instance, or from JSON. When a preset is
active for a media type it replaces the simple ranking and filtering settings
for that type; everything else about search stays the same.

Manage them under **Settings → Quality presets** (`/admin/quality-presets`).
Signed-in profiles can read presets and choose their own profiles' defaults
at `/account/quality-presets`, among the presets offered to them (see
[Per profile](#per-profile)); everything else needs the server administrator.

## How a preset ranks

For each result mediastorm parses the release name like the matching *arr
app (quality, revision, languages, release group, release type, edition) and
evaluates every custom format:

- conditions of the same type form a group; a group passes when all its
  required conditions hold and at least one of its conditions matches;
  negation applies per condition;
- a format matches when every group passes; the release score is the sum of
  the scores of the matched formats in the profile.

A release is filtered out when its quality is not allowed in the profile,
when its score is below the profile's minimum, when it lacks a Radarr
profile's language, or (if switched on) when its size is outside the
profile's size limits. The rest are sorted like the
*arr download decision: quality position (qualities in one group are equal),
then propers/repacks if the profile prefers them, custom format score,
preferred protocol, season pack and episode count, seeders, usenet age, and
size closest to the preferred size. Without a preferred protocol in the
preset, mediastorm's service priority picks it (debrid means torrent); with
neither, usenet comes first, as in *arr's default delay profile.

Three checks always come first, in any mode, because they protect
correctness or an explicit audio choice: **Preferred Audio Language**,
**Country Match** and **Episode Year**.

Language handling follows *arr: languages an indexer reports replace parsed
ones; a release without a language tag counts as the title's original
language (from metadata); "Original" in a format means the title's original
language; `MULTi` releases get the preset's multi-languages when they are set.

In preset mode the simple-mode preferences (size and resolution caps, HDR/DV
policy, required, blocked, preferred and non-preferred terms) are not
applied, since the preset expresses them. Device bandwidth caps (adaptive
playback), service priority, the unknown-track policy and the Real-Debrid
restricted terms filter still apply.

## Choosing the active preset

The first match wins:

1. **Per search**: a version picked in a client, or the `qualityPreset`
   parameter of `GET /api/indexers/search` (a preset ID or `simple`). Presets
   offered in version pickers are always allowed; any other preset only when
   **Apps may request any preset for a single search** is on. A profile with
   its own picker list may only request presets on that list.
2. **Title**: the preset the profile last played the title with on this
   device (see *Remembered per title and device* below).
3. **Anime titles only**: the anime slots of the device
   (`qualityPresetAnimeMovie`, `qualityPresetAnimeSeries`), the profile and
   the server, in that order.
4. **Device**: the client's filter settings (`qualityPresetMovie`,
   `qualityPresetSeries`).
5. **Profile**: the profile's assignment on the presets page.
6. **Server**: the presets selected for movies and series.

Nothing selected, a preset of the wrong app (a Sonarr preset for a movie)
or one whose audience does not fit the title falls back to the simple
ranking. Deleting a preset clears the server selection and the version
pickers that used it; a device or profile that chose it is skipped, so the
next level decides (the Per profile table shows it as "(deleted preset)").

### Remembered per title and device

A preset picked for playback (a preset version in a Jellyfin client, the
web player's selector, or any prequeue with `qualityPreset`) is
remembered for the profile, the device and the title, like a picked audio
language: for a series it applies to all its episodes, for a movie to that
movie, on that device. Another device of the profile keeps its own choice,
so a TV can stay on UHD while a phone plays the same series in HD. Auto
then ranks the title with it, so the Jellyfin bridge's "Auto" is named
after it ("Auto · German HD") and the next episode plays with it.
Picking another preset replaces it; picking Auto leaves it as it is.

Only presets that differ from the default are remembered. The default
is what the title gets on the device without a remembered preset (the
anime or normal slots of the device, the profile and the server), and
picking it forgets the remembered one: the title follows the defaults
again there, including later changes to them. The Jellyfin bridge marks
that version "Preset · German HD (default)", so viewers reset a title by
playing it once. `GET /api/quality-presets` reports it as `movieDefault`
and `seriesDefault` next to `movie` and `series` (the device is the
request's `clientId`).

It is stored in its own table (profile, device, title), apart from the
title's audio and subtitle preferences. The device is the client ID a
playback names (Jellyfin clients: `jellyfin-` and their device ID); a
request without one is its own "no device" entry. Only a preset the
request may use is remembered (it exists, is for the title's app and is
allowed, as for a search), and a remembered preset only counts while that
still holds: allowed for requests (offered by the server, or any preset
while "Apps may request any preset" is on), on the profile's picker list,
not deleted and for the title's app; otherwise the next level decides.
Backups of the server settings and user data do not include it.

### Fallback

A preset can reject every release a search finds: an UHD preset on a show
that only exists in 1080p, a German preset on a title without German audio,
a Remux preset for a streaming-only series. Without a fallback the search
then finds nothing and playback fails, as in Sonarr/Radarr (which wait for
a matching release instead).

On a preset's overview, **If nothing passes** sets what ranks such a search
instead: **Nothing** (the default), **Simple ranking settings**, or another
preset of the same app. A fallback preset can have its own fallback, up to
3 in a row; loops are refused. A fallback preset ranks the same results,
so there is no second indexer search. The simple ranking needs its own
filters: for playback (prequeue, Auto Select) it reranks the cached
results, while a release list (`/api/indexers/search`, Select Release, the
Jellyfin versions) that falls back to it searches the indexers once more.
A preset that passes
anything keeps its own ranking, so the episodes that do exist in UHD still
play in UHD. A fallback preset for the other app or one whose audience does
not fit the title ends the chain. Deleting a preset clears the fallbacks
pointing at it.

The log names each step (`[quality-preset] "UHD" passed nothing for
query="Dark S01E01" user="…"; using fallback "German HD"`). The remembered
preset of a title stays as it is: every search tries it first. In the
opt-in mode that resolves the first ready source, the fallback applies to
each source's results on their own, and the simple ranking there keeps the
preset mode's filters.

### Anime

A title is anime when its metadata has the genre *Anime*, or the genre
*Animation* together with a Japanese, Chinese or Korean original language,
original or alternate title in East Asian script, or an East Asian airing
timezone. Each decision is logged once with its reason (`[anime] "Title"
anime=true: genre Animation, original language ja`); the admin **Search**
tester shows it for the selected title.

Each preset has an **audience** (on its overview): *All titles*, *Anime
only* or *Everything but anime*. Profiles whose name contains "anime" (such
as TRaSH's `[German] Anime HD Bluray + WEB` or `[Anime] Remux-1080p`) are
imported as *Anime only*. Anime slots take *Anime only* and *All titles*
presets, normal slots *All titles* and *Everything but anime*.

Anime titles try the anime slots on every level before any normal slot, so
a device's normal series preset does not override the anime preset; set
the device's anime slot to change anime there. An empty anime slot falls
back to the normal slots. While a preset ranks, the anime language
preference from Search & quality is not applied: anime presets carry their
own language formats.

### Version pickers

Under **Offer in version pickers** the admin checks which presets (and the
simple ranking) viewers may pick for a single playback, in the order the
pickers show them (▲/▼ change it):

- **Jellyfin clients** (through the Jellyfin bridge) list each offered
  preset that fits the item as an extra version, "Preset · German HD",
  after "Auto" and before the individual releases. It plays like
  Auto: mediastorm searches, resolves and falls back as usual (including
  Auto: Avoid Video Transcoding), ranked by that preset, in its own play
  session and prequeue. Offered presets appear even when Release Versions
  is 0. "Auto" is named after the preset it uses for the item ("Auto ·
  [German] Anime HD Bluray + WEB"), or just "Auto" with the simple
  ranking; "Preset" marks a version picked with a specific preset, which
  can be the same one Auto uses. On a server without any preset the
  version keeps its name "Auto (mediastorm)" and items skip the preset
  lookup.
- **The web player** (**Playback → Web Player**) shows a **Quality preset**
  selector on the title's details (for **Auto Select** and **Select
  Release**) and on the release list, with the offered presets for the
  title's media type; it starts at the profile's choice, and on the
  release list a change re-runs the search. Each preset-ranked release shows its *arr
quality and custom format score; hover for the matched formats. The admin
**Search** tester has the same selector.

Which offered choices appear depends on the title:

| Audience | Anime title | Other title |
|---|---|---|
| Anime only | shown | hidden |
| All titles (and the simple ranking) | shown, unless **On anime titles, offer only anime presets** is on | shown |
| Everything but anime | hidden | shown |

### Per profile

The **Per profile** table sets each profile's defaults (movies, series and
the anime slots) and, under **In version pickers**, which of the offered
presets the profile gets. All are checked by default ("All offered"), which
also includes presets the admin offers later; unchecking some stores a
list ("2 of 4") and the profile's pickers show only those.

Profiles choose their own defaults on their account page, but only among
the presets they are offered (offered by the server and on their list);
the admin may assign any preset. A default that is no longer offered stays
in effect until someone changes it.

The admin can also mark offered presets **hidden by default** (next to
each one under **Offer in version pickers**): profiles then do not see
them in their pickers until they show them themselves. That is a
recommendation, not a limit.

A profile decides what its own version pickers show: signed in with its
account, it checks or unchecks the presets it is offered under **In
version pickers** ("3 of 6 shown"). Each preset shows the admin's
recommendation (shown or hidden); a profile's own choice overrides it and
stays, also when the admin changes the recommendation later, until the
profile drops it with **Follow recommendation** (or **Follow all
recommendations**) and saves. A profile can only narrow what the admin
offers it, never widen it. A hidden preset counts as not offered for that
profile: it is not in its pickers, cannot be its default, and a preset
remembered for a title is skipped while hidden. The admin sees how many
own choices a profile made next to its list.

### Library copies

A movie or episode one of the profile's local libraries has plays from there,
in every app: the mediastorm apps, the web client and Jellyfin clients through
the bridge (Continue watching and Next up too), since the server decides when
a playback starts. Version pickers offer *Library copy* first for such a title,
and Auto uses it. Picking another preset for the title streams it instead, then
and later (the pick is remembered for the title on that device, even when it is
the default preset); picking *Library copy* again plays the file and forgets the
pick. *Prefer library copies* under Active presets switches this off: playing a
title always searches. Plex and Jellyfin libraries do not count; their items play
through their server as before.

## Importing

- **TRaSH-Guides**: pick a profile and a score set (TRaSH's `trash_scores`
  name, defaulting to the profile's own). Custom format groups follow their
  TRaSH defaults (required groups always, default-on groups unless switched
  off); optional groups can be switched on later. Quality sizes come from
  TRaSH's quality-size data.
- **Sonarr / Radarr**: URL (with URL base if any) and API key, then a
  profile. Its formats, scores and quality definitions are copied. The API
  key is stored with the preset (masked in every response) so the preset can
  be re-synced from the instance; **Connection…** on the preset changes URL
  or key later. Redirects to another host are not followed, so the key
  stays with the instance.
- **JSON**: a preset exported from mediastorm, a TRaSH profile together with
  its custom formats, or a list of custom formats.

**Make default for Movies/Series** (on by default) makes the new preset the
server's preset for its media type, or its anime slot for an anime-only
preset (the checkbox names the slot);
**Offer it in version pickers** adds it to the pickers. Formats the profile
uses that the snapshot lacks are reported after the import. The first
TRaSH or Sonarr/Radarr import adds the hourly *Quality preset updates*
task.

## Editing

Every change is stored as an override on top of the upstream version and
shown next to the upstream value with a reset button: format scores,
switching formats off, own formats with their own conditions, quality order
and allowed qualities, minimum score, language, propers handling, size
limits, format groups, name, multi-languages, preferred protocol and whether
size limits are enforced.

The **Test** tab ranks release names you paste and shows quality, languages,
group, matched formats and the reasons a release was filtered.

## Keeping presets up to date

Each preset has its own sync settings:

| Setting | Options |
| --- | --- |
| Mode | **Apply automatically** (default). **Ask before applying**: updates wait for approval. **Only when I check.** **Pin to a version**: stays on one commit. |
| Check | Daily, weekly or manually. The scheduled task *Quality preset updates* runs hourly and checks the presets that are due. |
| Follow upstream for | Quality order and limits, format conditions, scores, format groups, quality sizes. Parts that don't follow keep their current version. Individual formats can override this. |
| New formats from upstream | Switch on, add switched off, or hold for review. |

Your overrides always survive an update. When upstream changes a value you
changed too, your value stays in effect and the field is listed as a
conflict (upstream before, upstream now, yours) to keep, take upstream or
edit. A format that disappears upstream is removed, unless you had changed
it; then it is kept as your own format. **Preview ranking change** shows how
a list of release names would rank before and after a waiting update.

A check that finds nothing this preset uses changed only records the new
version: no history entry, no waiting update, and search caches stay.
Conflicts are reported once per upstream value.

The last ten applied states are kept; rolling back restores one exactly,
optionally pinning the preset there.

### Snapshot sources

TRaSH data comes from configurable sources merged by priority (lower number
wins when two define the same `trash_id`): GitHub repositories (default
`TRaSH-Guides/Guides@master`, so forks work), plain archive URLs, and the
snapshot bundled with mediastorm, which is also the fallback for imports and
the catalog when GitHub is unreachable; an update check fails instead, so an
outage never looks like an upstream change. Of each downloaded archive only
`docs/json` is cached in the database (about 120 KB, twenty per source).

With more than one source, every format records the source it was taken
from. The formats table shows it, and marks a format with "overrides …"
when a lower-precedence source defines the same `trash_id` differently.
Hovering a format in the test bench shows its layer (profile, group or your
own), its source and whether the score is upstream's or yours.

## Backups

Server backups include presets with their history and pending updates. The
snapshot cache is not backed up; it is downloaded again when needed.

## API

All routes exist under `/admin/api/quality-presets` and
`/account/api/quality-presets`.

| Method and path | Who | Purpose |
| --- | --- | --- |
| `GET /` | signed in | List presets, the server selection, sources, profile assignments (`?userId=`) |
| `GET /fixed-checks` | signed in | The checks that always run first |
| `GET /license` | signed in | Licence of the bundled TRaSH-Guides data (MIT) |
| `GET /effective?userId=&clientId=` | signed in | Same as the app list below (used by the web player) |
| `PUT /assign` | own profiles, admin | `{"userId","clientId","moviePreset","seriesPreset","animeMoviePreset","animeSeriesPreset"}`; with `clientId` it sets the device choice; slots left out are kept, unknown fields are rejected; `"inherit"` clears, `"simple"` forces the simple ranking; profiles may only pick presets offered to them; `"pickerPresets"` (admin only) sets the profile's picker list: a list of preset IDs and `simple`, `null` for all offered, left out keeps it; `"hiddenPresets"` (the profile itself or the admin, not with `clientId`) lists offered presets the profile hides from its own pickers, `null` or `[]` hides none, left out keeps it; `"shownPresets"` (same rules) lists presets hidden by default that the profile shows; both lists empty means the profile follows the admin's recommendation |
| `GET /{id}` | signed in | Preset with its resolved profile and formats |
| `POST /{id}/explain` | signed in | `{"titles":[…],"originalLanguage","runtimeMinutes","detail"}` → ranked evaluation |
| `GET /catalog` | admin | TRaSH profiles and score sets of the current snapshot |
| `PUT /selection` | admin | `{"moviePreset","seriesPreset","animeMoviePreset","animeSeriesPreset","animeOnlyInPickers","allowRequestOverride","pickerPresets","hiddenByDefault"}`; fields left out are kept, unknown fields are rejected; `pickerPresets` lists preset IDs and `simple` in display order; `hiddenByDefault` lists offered presets profiles see only when they show them (entries that are not offered are dropped) |
| `GET, PUT /sources` | admin | Snapshot sources |
| `POST /sync` | admin | Check every preset now |
| `POST /import/trash` | admin | `{"app","profileTrashId","scoreSet","name","groups","sync"}` |
| `POST /import/arr/profiles` | admin | `{"url","apiKey"}` → the instance's profiles |
| `POST /import/arr` | admin | `{"url","apiKey","profileId","name"}` |
| `POST /import/json?app=` | admin | Body: preset, TRaSH profile or custom formats |
| `DELETE /{id}` | admin | Delete |
| `GET /{id}/export` | admin | Download as JSON (API keys removed) |
| `POST /{id}/duplicate` | admin | Copy as a manual preset |
| `PUT /{id}/settings` | admin | Name, audience (`""`, `anime`, `non-anime`), fallback (`""`, `simple` or a preset ID of the same app), sync settings, size limit enforcement, protocol, multi-languages, propers; `connection` (`{"url","apiKey"}`, an empty key keeps the stored one) for Sonarr/Radarr presets |
| `PUT, DELETE /{id}/override` | admin | `{"path","value"}`; paths `score:<format>`, `format:<format>`, `items`, `minFormatScore`, `language`, `propers`, `size:<quality>`, `group:<group>` |
| `POST /{id}/formats`, `DELETE /{id}/formats/{key}` | admin | Add or remove an own format |
| `PUT /{id}/formats/{key}/disabled` | admin | Switch a format on or off |
| `POST /{id}/check` | admin | Check for an update now |
| `POST, DELETE /{id}/pending` | admin | Apply or discard the waiting update |
| `POST /{id}/conflicts` | admin | `{"path","choice","value"}` with choice `yours`, `upstream` or `edit` |
| `POST /{id}/review` | admin | `{"keys","enable"}`: switch held new formats on or keep them off |
| `POST /{id}/rollback` | admin | `{"historyId","pin"}` |
| `POST /{id}/dry-run` | admin | Ranking before and after the waiting update |

Apps use the token-authenticated API:

| Method and path | Purpose |
| --- | --- |
| `GET /api/quality-presets?userId=&clientId=[&mediaType=&titleId=&name=&year=&imdbId=]` | Presets (id, name, app, media type, audience); with a title, `anime` and `animeReason`, the offered choices that fit it and an anime-aware selection; the choices `offered` for version pickers (in order, narrowed by the profile's list; `simple` has no media type), whether any preset may be requested per search, and the preset movies and series use for that profile and device right now (with where the choice came from: `device`, `profile`, `global` or `none`; an empty `presetId` means the simple ranking) |
| `PUT /api/quality-presets/assign` | Same body as above; with `clientId` the device must be registered for the profile |
| `GET /api/indexers/search?…&qualityPreset=` | Use a preset (or `simple`) for one search; an unknown preset answers 400, one the profile may not request 403 |

Search results ranked by a preset carry `qualityPreset`, `qualityPresetId`,
`arrQuality`, `customFormatScore` and `customFormats` attributes.

## Limitations

- **Indexer flags** (freeleech, internal and so on) are torrent-indexer data
  that usenet results don't have; conditions on them count as not matched
  and the preset lists them as unsupported.
- **Parity** with Sonarr 4 and Radarr 6 is measured on about 3,200 release
  names and enforced by tests (see `tools/arrparity`): quality, revision,
  languages, release group and matched custom formats agree on 99.6% or
  more. Radarr's edition parsing is the weakest field at about 98%. Ranked
  in groups of ten with four TRaSH profiles, the oracle's facts and
  mediastorm's pick the same release in every group.
- **Original language** comes from title metadata; without it, untagged
  releases count as language Unknown, as in *arr.
- **Size limits** need the runtime; when it is unknown they are not checked.

## Licensing

Sonarr and Radarr are GPL-3.0 licensed. mediastorm contains none of their
code: the parser and custom format engine are written from their documented
and observed behaviour and checked against running instances. TRaSH-Guides
data is MIT licensed; the bundled snapshot keeps its licence in
`internal/trash/bundle/LICENSE`, which is embedded in the binary and
linked from the Quality presets page.
