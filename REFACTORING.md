# File Refactoring Plan

Goal: Reduce large files to ~2000 lines by removing dead code and extracting helpers.

## Completed

### settings.tsx (5068 → 2914 lines) ✓
- Removed hidden tab code (playback, home, filtering, live, content, advanced)
- Removed unused grid data, modals, callbacks, and state
- Removed commented-out tab bar code
- Build verified working

**Optional further extraction (~850 lines):**
- Extract `createStyles()` to `styles/settings-styles.ts`
- Extract types to `types/settings.ts`
- Extract `toEditableSettings()` / `toBackendPayload()` to `utils/settings-helpers.ts`

### player.tsx (5184 → 4874 lines) ✓
- Extracted `createPlayerStyles()` to `styles/player-styles.ts` (~115 lines)
- Extracted types and helper functions to `utils/player-helpers.ts` (~195 lines):
  - Types: `ConsoleLevel`, `DebugLogEntry`, `TrackOption`, `PlayerParams`
  - Helpers: `parseBooleanParam`, `SUBTITLE_OFF_OPTION`, `toTitleCase`, `stripEpisodeCodeSuffix`, `formatLanguage`, `buildAudioTrackOptions`, `buildSubtitleTrackOptions`, `resolveSelectedTrackId`
- Build verified working

**Note:** Many components already extracted to `components/player/`:
- Controls, ExitButton, TVControlsModal, MediaInfoDisplay, StreamInfoModal
- SubtitleOverlay, SubtitleSearchModal, VideoPlayer, TrackSelectionModal, SeekBar

**Further extraction limited:** The remaining ~4800 lines are the tightly-coupled main component with extensive state management and effects for HLS session handling, playback progress, episode navigation, and platform-specific behaviors.

### details.tsx (4522 → 4126 lines) ✓
- Extracted `createDetailsStyles()` to `styles/details-styles.ts` (~396 lines)
- Build verified working

**Note:** Many components already extracted to `app/details/`:
- BulkWatchModal, ManualSelection, ResumePlaybackModal, TrailerModal
- SeasonSelector, EpisodeSelector, SeriesEpisodes
- playback helpers, track-selection, utils

### hls.go (4613 → 3707 lines) ✓
- Extracted probe functionality to `handlers/hls_probe.go` (~913 lines):
  - Types: `audioStreamInfo`, `subtitleStreamInfo`, `UnifiedProbeResult`, `cachedProbeEntry`
  - Helper: `isHLSCommentaryTrack`
  - Constant: `probeCacheTTL`
  - Cache methods: `GetCachedProbe`, `CacheProbe`, `cleanupProbeCache`
  - Probe functions: `probeAllMetadata`, `probeAllMetadataFromURL`, `parseUnifiedProbeOutput`
  - Audio probes: `probeAudioStreams`, `probeAudioStreamsFromURL`
  - Subtitle probes: `probeSubtitleStreams`, `probeSubtitleStreamsFromURL`
  - Duration probes: `probeDuration`, `probeDurationFromURL`
  - Color probes: `probeColorMetadata`, `probeColorMetadataFromURL`
- Build verified working

**Optional further extraction (~1700 lines to reach ~2000):**
- Extract segment logic to `hls_segments.go`
- Extract playlist logic to `hls_playlist.go`
- Extract subtitle handling to `hls_subtitles.go`

---

## Remaining Files

### 1. admin_ui.go (4880 lines)
**Likely extractable:**
- Handler groups by feature (settings, users, logs, etc.)
- Template rendering helpers
- Validation functions

**Approach:**
1. Group handlers by domain
2. Extract to separate handler files:
   - `handlers/admin_settings.go`
   - `handlers/admin_users.go`
   - `handlers/admin_logs.go`

### 2. tools.html (3607 lines) - Backend Template
**Likely extractable:**
- Split into partial templates
- Extract JavaScript to separate files

### 4. index.tsx (3389 lines) - Home Screen
**Likely extractable:**
- Shelf components
- Search functionality
- Styles

---

## Priority Order

1. **admin_ui.go** - Handler grouping
2. **index.tsx** - UI component extraction
3. **tools.html** - Backend template cleanup

---

## Guidelines

- Delete dead code entirely (recoverable from git)
- Extract styles to separate files first (easy wins)
- Create focused, single-responsibility modules
- Maintain existing patterns in codebase
- Verify build after each extraction
