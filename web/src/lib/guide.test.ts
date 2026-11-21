import { describe, expect, it } from 'vitest';
import { blockGeometry, chunk, guideStart, halfHourTicks, HOUR, PX_PER_MIN } from './guide';

describe('guide math', () => {
  const t0 = Date.parse('2026-11-21T18:00:00Z');

  it('starts half an hour before the current half hour', () => {
    expect(guideStart(t0 + 20 * 60_000)).toBe(t0 - 30 * 60_000);
  });

  it('clips programme blocks to the window', () => {
    expect(blockGeometry({ start: t0 - HOUR, stop: t0 + HOUR }, t0, t0 + 2 * HOUR)).toEqual({
      left: 0,
      width: 60 * PX_PER_MIN,
    });
    expect(blockGeometry({ start: t0 - 2 * HOUR, stop: t0 - HOUR }, t0, t0 + HOUR)).toBeNull();
  });

  it('lists half-hour ticks', () => {
    expect(halfHourTicks(t0, t0 + HOUR)).toEqual([t0, t0 + 30 * 60_000]);
  });

  it('chunks', () => {
    expect(chunk([1, 2, 3], 2)).toEqual([[1, 2], [3]]);
  });
});
