import { readJSON, writeJSON } from '@/api/storage';
import type { LiveView } from './LiveToolbar';

/**
 * The Live TV category selection and view, kept in this browser per profile and restored
 * when /live opens without filters. Both natives keep these on the device, not in the
 * profile's settings (tvOS LiveStore `categoriesKey(profileID)` + `@AppStorage("live.mode")`,
 * Android LivePrefs `categories.<profileId>`).
 */
export interface LiveMemory {
  categories: string[];
  view?: LiveView;
}

const key = (profileId: string) => `msweb.live.${profileId}`;

export function readLiveMemory(profileId: string): LiveMemory {
  const raw = readJSON<Partial<LiveMemory>>(key(profileId));
  const categories = Array.isArray(raw?.categories)
    ? raw.categories.filter((c): c is string => typeof c === 'string' && c !== '')
    : [];
  const view = raw?.view === 'guide' || raw?.view === 'channels' ? raw.view : undefined;
  return { categories, view };
}

export function writeLiveMemory(profileId: string, memory: LiveMemory): void {
  writeJSON(key(profileId), {
    categories: [...new Set(memory.categories)].sort(),
    ...(memory.view ? { view: memory.view } : {}),
  });
}
