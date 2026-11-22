import { afterEach, describe, expect, it } from 'vitest';
import { pickedPresetFor, resetWarmPrequeues, setPickedPreset } from './useWarmPrequeue';

describe('picked quality presets', () => {
  afterEach(() => resetWarmPrequeues());

  it('outlive the page, per profile and title', () => {
    setPickedPreset('p1', 'tmdb:tv:1', 'uhd');
    expect(pickedPresetFor('p1', 'tmdb:tv:1')).toBe('uhd');
    expect(pickedPresetFor('p2', 'tmdb:tv:1')).toBeUndefined();
    expect(pickedPresetFor('p1', 'tmdb:tv:2')).toBeUndefined();
    setPickedPreset('p1', 'tmdb:tv:1', 'simple');
    expect(pickedPresetFor('p1', 'tmdb:tv:1')).toBe('simple');
  });
});
