import { describe, expect, it } from 'vitest';
import { getPath, moveShelf, setPath, sortedShelves, type ShelfConfig } from './settings';

describe('setPath', () => {
  it('sets nested values immutably and keeps unknown fields', () => {
    const src = { display: { badgeVisibility: ['watchState'], other: 1 }, liveTV: { x: true } };
    const next = setPath(src, 'display.appearance.accentColor', '#ff0000');
    expect(next).toEqual({
      display: {
        badgeVisibility: ['watchState'],
        other: 1,
        appearance: { accentColor: '#ff0000' },
      },
      liveTV: { x: true },
    });
    expect(src.display).not.toHaveProperty('appearance');
    expect(getPath(next, 'display.appearance.accentColor')).toBe('#ff0000');
    expect(setPath(next, 'display.appearance.accentColor', undefined)).toEqual({
      display: { badgeVisibility: ['watchState'], other: 1, appearance: {} },
      liveTV: { x: true },
    });
  });
});

describe('shelves', () => {
  const shelf = (id: string, order: number): ShelfConfig => ({
    id,
    name: id,
    enabled: true,
    order,
  });
  it('sorts and renumbers, and moves', () => {
    const sorted = sortedShelves([shelf('b', 5), shelf('a', 2), shelf('c', 9)]);
    expect(sorted.map((s) => [s.id, s.order])).toEqual([
      ['a', 0],
      ['b', 1],
      ['c', 2],
    ]);
    expect(moveShelf(sorted, 2, -1).map((s) => s.id)).toEqual(['a', 'c', 'b']);
    expect(moveShelf(sorted, 0, -1)).toBe(sorted);
  });
});
