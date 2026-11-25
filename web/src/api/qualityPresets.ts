/**
 * Quality presets (servers with Sonarr/Radarr-style presets): the presets a version picker
 * offers for a title, and which one Play would use. Mirrors native-androidtv
 * core/.../api/QualityPresetsApi.kt; shapes match backend handlers/quality_presets_selection.go
 * (GET /api/quality-presets, clientPresetList).
 */
import { apiRequest } from './client';
import { getClientId } from './storage';

export interface QualityPreset {
  id: string;
  name: string;
  /** "movie", "series", or empty for the simple ranking, which fits both. */
  mediaType?: string;
}

export interface QualityPresetSelection {
  /** Empty when the simple ranking applies. */
  presetId?: string;
  name?: string;
  /** Where it comes from (title, device, profile, server, …). */
  source?: string;
}

export interface QualityPresetChoices {
  offered: QualityPreset[];
  /** What Play uses now for the title on this device (its remembered preset, else the default). */
  movie: QualityPresetSelection;
  series: QualityPresetSelection;
  /** What it would use without a remembered preset: picking it clears the title's memory. */
  movieDefault: QualityPresetSelection;
  seriesDefault: QualityPresetSelection;
  /** Titles a local library has play from there unless another preset is picked (server setting). */
  preferLibraryCopies?: boolean;
}

/** The ID the server uses for its simple ranking (no preset). */
export const SIMPLE_PRESET = 'simple';

/** The "Library copy" choice: the title plays from a local library (offered only when one has it). */
export const LIBRARY_PRESET = 'local';

export interface PresetTarget {
  titleId: string;
  name: string;
  mediaType: 'movie' | 'series';
  imdbId?: string;
  tmdbId?: number | string;
  tvdbId?: number | string;
  year?: number;
  /** The episode about to play: "Library copy" is offered for that episode. */
  season?: number;
  episode?: number;
  /** Its absolute number (anime), for files numbered that way. */
  absolute?: number;
}

/** The presets offered for a movie or a series. */
export function offeredFor(c: QualityPresetChoices, mediaType: 'movie' | 'series') {
  return (c.offered ?? []).filter((p) => !p.mediaType || p.mediaType === mediaType);
}

/** The offered preset a selection names ("simple" for the simple ranking). */
export const presetIdOf = (s?: QualityPresetSelection) => s?.presetId || SIMPLE_PRESET;

export const currentPreset = (c: QualityPresetChoices, mediaType: 'movie' | 'series') =>
  mediaType === 'movie' ? c.movie : c.series;

export const defaultPreset = (c: QualityPresetChoices, mediaType: 'movie' | 'series') =>
  mediaType === 'movie' ? c.movieDefault : c.seriesDefault;

/**
 * Which library copies Play takes, as the server decides for every other app: all of them when
 * "Library copy" is picked here or used by Auto (and without presets, as before); none when
 * another preset is picked for the title; with the server setting off only Plex and Jellyfin
 * copies, which the setting does not cover.
 */
export function libraryCopyRule(
  c: QualityPresetChoices | null | undefined,
  mediaType: 'movie' | 'series',
  picked?: string,
): 'all' | 'remote' | 'none' {
  if (!c) return 'all';
  if (c.preferLibraryCopies === false) return 'remote';
  // Without a local copy the choice is not offered: picks are about ranking, the Plex or
  // Jellyfin copy plays as before.
  if (!offeredFor(c, mediaType).some((p) => p.id === LIBRARY_PRESET)) return 'all';
  if (picked) return picked === LIBRARY_PRESET ? 'all' : 'none';
  return currentPreset(c, mediaType).presetId === LIBRARY_PRESET ? 'all' : 'none';
}

/** The copies a rule lets Play take (remote: Plex and Jellyfin ones). */
export function copiesFor<T extends { item: { sourceType?: string } }>(
  copies: T[],
  rule: 'all' | 'remote' | 'none',
): T[] {
  if (rule === 'all') return copies;
  if (rule === 'none') return [];
  return copies.filter((v) =>
    ['plex', 'jellyfin'].includes((v.item.sourceType ?? '').toLowerCase()),
  );
}

/**
 * The quality presets offered for a title on this profile and device, or null when the server
 * has none to offer (no presets, or a server without them: errors count as none).
 */
export async function getQualityPresets(
  profileId: string,
  t: PresetTarget,
  signal?: AbortSignal,
): Promise<QualityPresetChoices | null> {
  try {
    const c = await apiRequest<QualityPresetChoices>('/quality-presets', {
      signal,
      query: {
        userId: profileId,
        clientId: getClientId(),
        mediaType: t.mediaType,
        titleId: t.titleId,
        name: t.name,
        imdbId: t.imdbId,
        tmdbId: t.tmdbId !== undefined ? String(t.tmdbId) : undefined,
        tvdbId: t.tvdbId !== undefined ? String(t.tvdbId) : undefined,
        year: t.year,
        season: t.episode ? t.season : undefined,
        episode: t.episode,
        absolute: t.episode && t.absolute ? t.absolute : undefined,
      },
    });
    // Kept without offered presets too when it switches library copies off, which the
    // library rule needs.
    return c && (offeredFor(c, t.mediaType).length > 0 || c.preferLibraryCopies === false)
      ? c
      : null;
  } catch (err) {
    if (signal?.aborted) throw err;
    return null;
  }
}

export const qualityPresetKeys = {
  title: (profileId: string, titleId: string, mediaType: string, episode?: string) =>
    ['qualityPresets', profileId, mediaType, titleId, episode ?? ''] as const,
};
