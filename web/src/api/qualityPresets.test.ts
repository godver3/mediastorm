import { describe, expect, it } from 'vitest';
import { prequeueBody } from '@/features/playback/resolve';
import {
  currentPreset,
  offeredFor,
  presetIdOf,
  SIMPLE_PRESET,
  type QualityPresetChoices,
} from './qualityPresets';

const choices: QualityPresetChoices = {
  offered: [
    { id: SIMPLE_PRESET, name: 'Simple ranking' },
    { id: 'm1', name: 'UHD Bluray + WEB', mediaType: 'movie' },
    { id: 's1', name: 'WEB-2160p', mediaType: 'series' },
  ],
  movie: { presetId: 'm1', name: 'UHD Bluray + WEB', source: 'title' },
  series: {},
  movieDefault: { presetId: '', name: 'Simple ranking' },
  seriesDefault: { presetId: 's1', name: 'WEB-2160p' },
};

describe('quality presets', () => {
  it('offers the presets for the media type plus the simple ranking', () => {
    expect(offeredFor(choices, 'movie').map((p) => p.id)).toEqual([SIMPLE_PRESET, 'm1']);
    expect(offeredFor(choices, 'series').map((p) => p.id)).toEqual([SIMPLE_PRESET, 's1']);
  });

  it('names the simple ranking "simple"', () => {
    expect(presetIdOf(currentPreset(choices, 'movie'))).toBe('m1');
    expect(presetIdOf(currentPreset(choices, 'series'))).toBe(SIMPLE_PRESET);
  });

  it('sends a picked preset with the prequeue, and none otherwise', () => {
    const title = { titleId: 'tmdb:movie:1', name: 'Movie', mediaType: 'movie' as const };
    expect(prequeueBody({ title, qualityPreset: 'm1' }).qualityPreset).toBe('m1');
    expect(prequeueBody({ title })).not.toHaveProperty('qualityPreset');
  });
});
