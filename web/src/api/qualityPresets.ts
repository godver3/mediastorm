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
}

/** The ID the server uses for its simple ranking (no preset). */
export const SIMPLE_PRESET = 'simple';

export interface PresetTarget {
  titleId: string;
  name: string;
  mediaType: 'movie' | 'series';
  imdbId?: string;
  tmdbId?: number | string;
  tvdbId?: number | string;
  year?: number;
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
      },
    });
    return c && offeredFor(c, t.mediaType).length > 0 ? c : null;
  } catch (err) {
    if (signal?.aborted) throw err;
    return null;
  }
}

export const qualityPresetKeys = {
  title: (profileId: string, titleId: string, mediaType: string) =>
    ['qualityPresets', profileId, mediaType, titleId] as const,
};
