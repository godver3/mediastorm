# Local Media Frontend Plan

## Current State

The backend already has a working local media scan and playback foundation. The missing piece is not basic API support, but a consumer-facing browse/details layer for the main frontend app.

Existing management endpoints:

- `GET /admin/api/library/libraries`
- `POST /admin/api/library/libraries`
- `DELETE /admin/api/library/libraries/{libraryID}`
- `POST /admin/api/library/libraries/{libraryID}/scan`
- `GET /admin/api/library/libraries/{libraryID}/items`
- `GET /admin/api/library/search`
- `GET /admin/api/library/fs`
- `PUT /admin/api/library/items/{itemID}/match`
- `DELETE /admin/api/library/items/{itemID}`

Equivalent account-scoped routes also exist under `/account/api/library/...`.

Existing playback endpoint:

- Local media playback is already exposed through `LocalMediaHandler.GetPlayback`, which returns stream URLs for a specific local media item.
- The player path ultimately resolves to a `localmedia:{itemID}/{filename}` stream path and then into the normal video streaming/HLS handlers.

Current limitations:

- There is no main-app browsing surface for local content.
- There is no frontend-native local media shelf, library tab, or detail screen flow.
- The existing item list API is oriented toward scan management and cleanup, not a consumer browsing experience.
- The current list response does not provide dedicated grouping concepts such as shows, seasons, or collections.
- Authorization and UX are still tied to admin/account management patterns rather than content discovery patterns.

## Goal

Expose local media as a first-class content source in the main frontend, with a UX that feels consistent with the rest of the app rather than like an admin tool.

The target experience should support:

- Browsing local movies and local shows from the main frontend.
- Entering a local title detail page that looks and behaves like remote metadata-backed details.
- Playing a local item through the existing playback pipeline.
- Showing missing state for scanned-but-no-longer-present items only in admin/maintenance surfaces, not in normal user browsing.
- Reusing existing matched metadata when available so local content can participate in posters, backdrops, credits, ratings, trailers, and related-content UI where possible.

## Recommended Product Shape

### 1. Separate Consumer APIs from Admin APIs

Do not build the main frontend directly against the current admin/account library management endpoints, even though those endpoints already exist and work for maintenance use cases.

Instead, add dedicated consumer-facing endpoints for local content discovery and reuse the existing playback pipeline, for example:

- `GET /api/local-media/libraries`
- `GET /api/local-media/browse`
- `GET /api/local-media/titles/{titleID}`
- `GET /api/local-media/items/{itemID}/playback`

These should return data shaped for browsing and playback, not scan administration.

### 2. Treat Matched Metadata as the Canonical Browse Layer

For frontend browsing, the main unit should usually be a matched title, not a raw file row.

Recommended browse entities:

- Local movie title
- Local series title
- Local episode
- Local variant/file

This allows the frontend to show a single title card for a movie or show even if multiple file variants exist.

### 3. Keep Raw File Rows in Admin Only

The current library item rows are still useful for:

- scan quality review
- rematching
- missing-item cleanup
- file-level diagnostics
- codec/probe inspection

That should remain an admin/account maintenance surface, separate from consumer browsing.

## Proposed API Evolution

### Phase 1: Read-Only Consumer Browse API

Add a backend read model over the already-scanned items that produces frontend-ready local titles.

Suggested endpoints:

- `GET /api/local-media/home`
  Returns lightweight shelves such as `recentlyAdded`, `movies`, `shows`, `continueWatching`.

- `GET /api/local-media/movies`
  Paginated local movie browse.

- `GET /api/local-media/shows`
  Paginated local show browse.

- `GET /api/local-media/titles/{id}`
  Returns full local title details plus available files/episodes.

- `GET /api/local-media/items/{itemID}/playback`
  Reuse the current playback response shape behind a frontend-facing route namespace, or formally adopt the existing local playback endpoint if its auth model and route naming are acceptable.

### Phase 2: Unified Details Behavior

For matched local titles, the frontend should be able to open a detail screen that looks like the existing details experience.

The backend should provide:

- the matched metadata title
- local availability summary
- available local files or episodes
- preferred/default file selection
- playback targets for the selected file

This can either be:

- a dedicated local details payload, or
- an extension of the existing details bundle with a local availability section

### Phase 3: Continue Watching and Watch State Integration

Local content should participate in:

- continue watching
- playback progress
- watch history
- resume prompts

This should use stable local item IDs while also carrying external IDs when metadata is matched.

### Phase 4: Search Integration

Local content should appear in app search as a source alongside remote content.

Recommended behavior:

- matched local titles rank near the corresponding metadata result
- local-only unmatched items can still be searched, but should be visually marked as local/manual

## Data Shape Recommendations

For consumer APIs, prefer a title-centric response like:

- `id`
- `kind` (`movie` or `series`)
- `name`
- `year`
- `poster`
- `backdrop`
- `overview`
- `externalIds`
- `localAvailability`
- `playable`

Where `localAvailability` can contain:

- `libraryId`
- `itemCount`
- `missingItemCount`
- `hasPlayableFiles`
- `bestMovieItemId`
- `seasons`
- `episodesAvailable`

For file-level drill-down, return:

- `itemId`
- `relativePath`
- `matchStatus`
- `isMissing`
- `missingSince`
- `probe`
- `externalIds`

## UX Recommendations

### Main Frontend

- Add a `Local` destination or shelf entry point.
- Show matched poster art when available.
- Hide missing items from normal browse.
- Prefer title-level browsing over file-level browsing.
- Let playback default to the best available local file with optional manual variant selection later.

### Admin/Account Library UI

- Keep current file-level management.
- Keep rematch and cleanup tools.
- Keep missing-item visibility and deletion here.

## Auth and Access Model

Decide early whether local media should be:

- available to all profiles by default, or
- controlled per account/profile/library

Recommendation:

- Start with account-level visibility for all profiles within the account.
- Add profile-level restrictions only if there is a clear product need.

## Suggested Backend Work Order

1. Add frontend-facing local media read endpoints over the existing scan data.
2. Add a title aggregation layer over `local_media_items`.
3. Decide whether to keep the current playback endpoint shape or expose it under a stable frontend route namespace.
4. Integrate local titles into existing details and continue-watching flows.
5. Add frontend browse screens and search integration.
6. Revisit whether admin/account management routes should be narrowed or renamed for clarity.

## Commit Plan

Recommended commit sequence once implementation starts:

1. `docs(backend): add local media frontend access plan`
2. `feat(backend): add consumer local media browse endpoints`
3. `feat(backend): add local media title aggregation model`
4. `feat(frontend): add local media browse entry point`
5. `feat(frontend): add local media details and playback flow`
6. `feat(frontend): integrate local media into search and continue watching`

## Deferred Cleanup

Later cleanup worth considering:

- Deprecate or remove legacy database layers that are no longer part of the active datastore path.
- Consolidate route naming so admin-management and consumer-content APIs are clearly separated.
- Add indexes specifically for any new local browse query patterns once the consumer API shape is settled.
