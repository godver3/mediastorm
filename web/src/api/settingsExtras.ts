import type { ShelfConfig } from './settings';

/**
 * Helpers for the extra per-profile settings sections (models/user_settings.go, models/ranking.go):
 * filter terms, ranking, menu tabs (native MenuTabs.swift), calendar per-shelf toggles and the
 * Home shelf editor.
 */

// MARK: filtering & ranking

export const FILTER_TERM_LISTS: { key: string; label: string; description: string }[] = [
  {
    key: 'requiredTerms',
    label: 'Required terms',
    description: 'A result must contain at least one of these to be kept.',
  },
  {
    key: 'filterOutTerms',
    label: 'Excluded terms',
    description: 'Results whose name contains any of these are removed.',
  },
  {
    key: 'preferredTerms',
    label: 'Preferred terms',
    description: 'Results with these rank higher.',
  },
  {
    key: 'nonPreferredTerms',
    label: 'Non-preferred terms',
    description: 'Results with these rank lower but are kept.',
  },
];

export interface RankingCriterion {
  id: string;
  enabled?: boolean;
  order?: number;
}

export const RANKING_CRITERIA: { id: string; label: string }[] = [
  { id: 'service-priority', label: 'Service priority' },
  { id: 'preferred-terms', label: 'Preferred terms' },
  { id: 'non-preferred-terms', label: 'Non-preferred terms' },
  { id: 'resolution', label: 'Resolution' },
  { id: 'language', label: 'Language' },
  { id: 'size', label: 'Size' },
  { id: 'preferred-scraper', label: 'Preferred scraper' },
];

export const criterionLabel = (id: string) =>
  RANKING_CRITERIA.find((c) => c.id === id)?.label ?? id;

/** The criteria in effect order (unknown ids kept, missing ones appended enabled), renumbered. */
export function rankingCriteria(raw: RankingCriterion[] | undefined): Required<RankingCriterion>[] {
  const listed = [...(raw ?? [])]
    .filter((c) => c && typeof c.id === 'string')
    .sort((a, b) => (a.order ?? 999) - (b.order ?? 999));
  const ids = new Set(listed.map((c) => c.id));
  const all: RankingCriterion[] = [
    ...listed,
    ...RANKING_CRITERIA.filter((c) => !ids.has(c.id)).map((c) => ({ id: c.id })),
  ];
  return all.map((c, i) => ({ id: c.id, enabled: c.enabled !== false, order: i }));
}

export function moveItem<T extends { order: number }>(
  list: T[],
  index: number,
  delta: -1 | 1,
): T[] {
  const target = index + delta;
  if (target < 0 || target >= list.length) return list;
  const next = [...list];
  [next[index], next[target]] = [next[target]!, next[index]!];
  return next.map((c, i) => ({ ...c, order: i }));
}

// MARK: menu tabs (display.navigationTabVisibility)

/** Tabs a profile can hide (native MenuTab). Home and Settings always show. */
export const MENU_TABS: { key: string; label: string }[] = [
  { key: 'search', label: 'Search' },
  { key: 'watchlist', label: 'Watchlist' },
  { key: 'live', label: 'Live TV' },
  { key: 'recordings', label: 'Recordings' },
  { key: 'calendar', label: 'Calendar' },
  { key: 'lists', label: 'Lists' },
  { key: 'sports', label: 'Sports' },
];

/** The server default (models.DefaultUserSettings), used when a profile sends none. */
export const DEFAULT_NAV_TABS = ['home', 'watchlist', 'search', 'lists', 'profiles', 'settings'];

/** An empty or missing list means unset (Expo toVisibilityMap). */
export function navTabs(raw: unknown): string[] {
  return Array.isArray(raw) && raw.length > 0
    ? raw.filter((x): x is string => typeof x === 'string')
    : DEFAULT_NAV_TABS;
}

export function setNavTab(tabs: string[], key: string, visible: boolean): string[] {
  const rest = tabs.filter((t) => t !== key);
  return visible ? [...rest, key] : rest;
}

// MARK: calendar per-shelf (calendar.mdblistShelves; missing = enabled)

/** Shelves the calendar reads (services/calendar collectFromMDBLists): enabled MDBList shelves. */
export const calendarShelves = (shelves: ShelfConfig[] | undefined) =>
  (shelves ?? []).filter(
    (s) =>
      s.type === 'mdblist' &&
      s.enabled &&
      typeof s.listUrl === 'string' &&
      !!s.listUrl &&
      !s.listUrl.startsWith('mediastorm:'),
  );

export const shelfCalendarEnabled = (map: Record<string, boolean> | undefined, id: string) =>
  map?.[id] !== false;

// MARK: home shelves editor

export const isMdblistUrl = (url: string) =>
  /^https:\/\/(www\.)?mdblist\.com\/lists\/.+/i.test(url.trim());

/** A new MDBList shelf (models.ShelfConfig type "mdblist"), appended at `order`. */
export function newListShelf(name: string, listUrl: string, order: number, now = Date.now()) {
  return {
    id: `mdblist-${now.toString(36)}`,
    name: name.trim() || 'My list',
    enabled: true,
    order,
    type: 'mdblist',
    listUrl: listUrl.trim(),
  } satisfies ShelfConfig;
}

/** Built-in shelves can be hidden but not removed. */
export const isRemovableShelf = (s: ShelfConfig) => !!s.type && s.type !== 'builtin';
