import { describe, expect, it } from 'vitest';
import { visibleNavItems } from '@/components/shell/nav';
import {
  calendarShelves,
  isMdblistUrl,
  isRemovableShelf,
  moveItem,
  navTabs,
  newListShelf,
  rankingCriteria,
  setNavTab,
  shelfCalendarEnabled,
} from './settingsExtras';

describe('ranking criteria', () => {
  it('keeps saved order, appends missing defaults enabled and renumbers', () => {
    const list = rankingCriteria([
      { id: 'size', order: 1, enabled: false },
      { id: 'resolution', order: 0 },
    ]);
    expect(list.slice(0, 3)).toEqual([
      { id: 'resolution', enabled: true, order: 0 },
      { id: 'size', enabled: false, order: 1 },
      { id: 'service-priority', enabled: true, order: 2 },
    ]);
    expect(list).toHaveLength(7);
  });

  it('moves within bounds', () => {
    const list = rankingCriteria(undefined);
    expect(moveItem(list, 0, -1)).toBe(list);
    expect(
      moveItem(list, 0, 1)
        .map((c) => c.id)
        .slice(0, 2),
    ).toEqual(['preferred-terms', 'service-priority']);
  });
});

describe('menu tabs', () => {
  it('treats an empty list as the server default', () => {
    expect(navTabs([])).toContain('watchlist');
    expect(navTabs(undefined)).not.toContain('live');
    expect(navTabs(['home', 'live'])).toEqual(['home', 'live']);
  });

  it('toggles a tab without duplicates', () => {
    expect(setNavTab(['home', 'live'], 'live', true)).toEqual(['home', 'live']);
    expect(setNavTab(['home', 'live'], 'live', false)).toEqual(['home']);
  });

  it('applies visibility to the sidebar; Home and Libraries always show', () => {
    const labels = (tabs?: string[], kids = false) =>
      visibleNavItems(kids, tabs).map((i) => i.label);
    expect(labels(undefined)).toHaveLength(9);
    expect(labels(['search'])).toEqual(['Home', 'Search', 'Libraries']);
    expect(labels(['live', 'sports'], true)).toEqual(['Home', 'Sports', 'Libraries']);
  });
});

describe('calendar shelves', () => {
  it('lists enabled MDBList shelves with a list URL', () => {
    const shelves = [
      {
        id: 'a',
        name: 'A',
        enabled: true,
        order: 0,
        type: 'mdblist',
        listUrl: 'https://mdblist.com/lists/x/y',
      },
      {
        id: 'b',
        name: 'B',
        enabled: false,
        order: 1,
        type: 'mdblist',
        listUrl: 'https://mdblist.com/lists/x/z',
      },
      {
        id: 'c',
        name: 'C',
        enabled: true,
        order: 2,
        type: 'mdblist',
        listUrl: 'mediastorm:list:1',
      },
      { id: 'd', name: 'D', enabled: true, order: 3, type: 'builtin' },
    ];
    expect(calendarShelves(shelves).map((s) => s.id)).toEqual(['a']);
    expect(shelfCalendarEnabled(undefined, 'a')).toBe(true);
    expect(shelfCalendarEnabled({ a: false }, 'a')).toBe(false);
  });
});

describe('shelf editor', () => {
  it('builds an MDBList shelf and only removes non-builtins', () => {
    expect(isMdblistUrl('https://mdblist.com/lists/user/top')).toBe(true);
    expect(isMdblistUrl('http://example.com')).toBe(false);
    const s = newListShelf('  ', ' https://mdblist.com/lists/u/l ', 4, 36);
    expect(s).toEqual({
      id: 'mdblist-10',
      name: 'My list',
      enabled: true,
      order: 4,
      type: 'mdblist',
      listUrl: 'https://mdblist.com/lists/u/l',
    });
    expect(isRemovableShelf(s)).toBe(true);
    expect(isRemovableShelf({ id: 'x', name: 'x', enabled: true, order: 0, type: 'builtin' })).toBe(
      false,
    );
  });
});
