import { apiRequest } from './client';
import type { UserProfile } from './types';
import { usersKeys } from './users';

/**
 * Per-profile settings (GET/PUT /api/users/{id}/settings, models/user_settings.go). PUT replaces
 * the whole document, so the page keeps the raw object it read and writes it back with only the
 * edited paths changed — fields this client does not know survive.
 */

export type RawSettings = Record<string, unknown>;

export interface ShelfConfig {
  id: string;
  name: string;
  enabled: boolean;
  order: number;
  type?: string;
  [extra: string]: unknown;
}

/** The fields the web settings page edits (all optional; the server fills defaults). */
export interface UserSettingsView {
  playback?: {
    preferredAudioLanguage?: string;
    preferredSubtitleLanguage?: string;
    preferredSubtitleMode?: string;
    disablePrequeue?: boolean;
    creditsAutoSkip?: boolean;
    autoPlayTrailersTV?: boolean;
  };
  filtering?: { maxResolution?: string; hdrDvPolicy?: string };
  display?: { appearance?: { accentColor?: string }; badgeVisibility?: string[] };
  homeShelves?: { shelves?: ShelfConfig[] };
  calendar?: {
    watchlist?: boolean;
    history?: boolean;
    trending?: boolean;
    topTrending?: boolean;
    mdblists?: boolean;
  };
}

/** Shares the shell's key so saving updates the live accent (useAccent). */
export const settingsKeys = {
  profile: (profileId: string) => usersKeys.settings(profileId),
};

const seg = encodeURIComponent;

export const getSettings = (profileId: string, signal?: AbortSignal) =>
  apiRequest<RawSettings>(`/users/${seg(profileId)}/settings`, { signal });

export const putSettings = (profileId: string, settings: RawSettings) =>
  apiRequest<RawSettings>(`/users/${seg(profileId)}/settings`, { method: 'PUT', body: settings });

/** Immutable set of a dotted path ('display.appearance.accentColor'); undefined deletes the key. */
export function setPath(obj: RawSettings, path: string, value: unknown): RawSettings {
  const [head, ...rest] = path.split('.');
  const key = head!;
  const next: RawSettings = { ...obj };
  if (rest.length === 0) {
    if (value === undefined) delete next[key];
    else next[key] = value;
    return next;
  }
  const child = obj[key];
  next[key] = setPath(
    child && typeof child === 'object' && !Array.isArray(child) ? (child as RawSettings) : {},
    rest.join('.'),
    value,
  );
  return next;
}

export function getPath<T>(obj: RawSettings | undefined, path: string): T | undefined {
  let cur: unknown = obj;
  for (const key of path.split('.')) {
    if (!cur || typeof cur !== 'object') return undefined;
    cur = (cur as RawSettings)[key];
  }
  return cur as T | undefined;
}

/** Shelves sorted by `order`, then renumbered 0..n (what the server stores). */
export function sortedShelves(shelves: ShelfConfig[] | undefined): ShelfConfig[] {
  return [...(shelves ?? [])].sort((a, b) => a.order - b.order).map((s, i) => ({ ...s, order: i }));
}

export function moveShelf(shelves: ShelfConfig[], index: number, delta: -1 | 1): ShelfConfig[] {
  const target = index + delta;
  if (target < 0 || target >= shelves.length) return shelves;
  const next = [...shelves];
  [next[index], next[target]] = [next[target]!, next[index]!];
  return next.map((s, i) => ({ ...s, order: i }));
}

// Profile (handlers/users.go)
export const renameProfile = (profileId: string, name: string) =>
  apiRequest<UserProfile>(`/users/${seg(profileId)}`, { method: 'PATCH', body: { name } });
export const setProfileColor = (profileId: string, color: string) =>
  apiRequest<UserProfile>(`/users/${seg(profileId)}/color`, { method: 'PUT', body: { color } });
export const setProfilePin = (profileId: string, pin: string) =>
  apiRequest<UserProfile>(`/users/${seg(profileId)}/pin`, { method: 'PUT', body: { pin } });
export const clearProfilePin = (profileId: string) =>
  apiRequest<UserProfile>(`/users/${seg(profileId)}/pin`, { method: 'DELETE' });

/** Track languages the native apps offer (SubtitleSearchModel.options settingCode). */
export const TRACK_LANGUAGES: { code: string; name: string }[] = [
  ['eng', 'English'],
  ['fra', 'French'],
  ['spa', 'Spanish'],
  ['deu', 'German'],
  ['ita', 'Italian'],
  ['por', 'Portuguese'],
  ['nld', 'Dutch'],
  ['pol', 'Polish'],
  ['rus', 'Russian'],
  ['jpn', 'Japanese'],
  ['kor', 'Korean'],
  ['zho', 'Chinese'],
  ['ara', 'Arabic'],
  ['heb', 'Hebrew'],
  ['swe', 'Swedish'],
  ['nor', 'Norwegian'],
  ['dan', 'Danish'],
  ['fin', 'Finnish'],
  ['hrv', 'Croatian'],
  ['srp', 'Serbian'],
  ['bos', 'Bosnian'],
].map(([code, name]) => ({ code: code!, name: name! }));

export const ACCENT_PRESETS = [
  '#3f66ff',
  '#7c4dff',
  '#e0567a',
  '#ef5350',
  '#ff8a3d',
  '#f1c40f',
  '#2ecc71',
  '#14b8a6',
  '#38bdf8',
  '#a3a7ba',
];

export const BADGES: { id: string; label: string }[] = [
  { id: 'watchProgress', label: 'Watch progress' },
  { id: 'watchState', label: 'Watch state' },
  { id: 'releaseStatus', label: 'Release status' },
  { id: 'unwatchedCount', label: 'Unwatched count' },
];
