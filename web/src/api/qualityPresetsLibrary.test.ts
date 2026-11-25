import { describe, expect, it } from 'vitest';
import {
  copiesFor,
  LIBRARY_PRESET,
  libraryCopyRule,
  type QualityPresetChoices,
} from './qualityPresets';

const choices = (current: string, prefer = true): QualityPresetChoices => ({
  offered: [
    { id: LIBRARY_PRESET, name: 'Library copy', mediaType: 'series' },
    { id: 'simple', name: 'Simple ranking' },
  ],
  movie: {},
  series: { presetId: current },
  movieDefault: {},
  seriesDefault: { presetId: LIBRARY_PRESET },
  preferLibraryCopies: prefer,
});

describe('libraryCopyRule', () => {
  it('plays library copies when Auto uses them, streams after another pick', () => {
    expect(libraryCopyRule(choices(LIBRARY_PRESET), 'series')).toBe('all');
    expect(libraryCopyRule(choices('simple'), 'series')).toBe('none');
    expect(libraryCopyRule(choices(LIBRARY_PRESET), 'series', 'simple')).toBe('none');
    expect(libraryCopyRule(choices('simple'), 'series', LIBRARY_PRESET)).toBe('all');
  });
  it('keeps Plex and Jellyfin copies with the server setting off; without presets all', () => {
    expect(libraryCopyRule(choices(LIBRARY_PRESET, false), 'series')).toBe('remote');
    expect(libraryCopyRule(null, 'series')).toBe('all');
    const noOffer: QualityPresetChoices = { ...choices('p1'), offered: [] };
    expect(libraryCopyRule(noOffer, 'series')).toBe('all');
    const copies = [{ item: { sourceType: 'plex' } }, { item: {} }];
    expect(copiesFor(copies, 'remote')).toEqual([{ item: { sourceType: 'plex' } }]);
    expect(copiesFor(copies, 'none')).toEqual([]);
    expect(copiesFor(copies, 'all')).toHaveLength(2);
    // A preset picked with the setting off keeps Plex and Jellyfin copies.
    expect(libraryCopyRule(choices(LIBRARY_PRESET, false), 'series', 'simple')).toBe('remote');
    // Without a local copy (not offered) a pick leaves the library copies alone.
    expect(libraryCopyRule(noOffer, 'series', 'simple')).toBe('all');
  });
});
