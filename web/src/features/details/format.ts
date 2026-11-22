import type { Episode, Season } from '@/api/details';
import type { Rating, Title } from '@/api/titles';

/** "2h 12m" / "48m". */
export function formatRuntime(minutes?: number): string | undefined {
  if (!minutes || minutes <= 0) return undefined;
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h ? (m ? `${h}h ${m}m` : `${h}h`) : `${m}m`;
}

/** Info line under the title: year, runtime, certification, genres (first three). */
export function infoLine(t: Title, opts: { seasons?: number } = {}): string[] {
  const parts: string[] = [];
  if (t.year) parts.push(String(t.year));
  if (opts.seasons) parts.push(opts.seasons === 1 ? '1 season' : `${opts.seasons} seasons`);
  const rt = t.mediaType === 'movie' ? formatRuntime(t.runtimeMinutes) : undefined;
  if (rt) parts.push(rt);
  if (t.certification) parts.push(t.certification);
  return parts;
}

const RATING_LABELS: Record<string, string> = {
  imdb: 'IMDb',
  tmdb: 'TMDB',
  trakt: 'Trakt',
  tomatoes: 'Rotten Tomatoes',
  audience: 'Audience',
  metacritic: 'Metacritic',
  letterboxd: 'Letterboxd',
};

const RATING_ORDER = ['imdb', 'tomatoes', 'audience', 'metacritic', 'letterboxd', 'trakt', 'tmdb'];

export interface RatingChip {
  source: string;
  label: string;
  text: string;
}

/** Ratings with a value, in a stable order, formatted on their own scale. */
export function ratingChips(ratings?: Rating[]): RatingChip[] {
  return (ratings ?? [])
    .filter((r) => typeof r.value === 'number' && r.value > 0)
    .sort((a, b) => {
      const ia = RATING_ORDER.indexOf(a.source);
      const ib = RATING_ORDER.indexOf(b.source);
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib);
    })
    .map((r) => {
      const v = r.value!;
      const max = r.max ?? 10;
      const text =
        max === 100 ? `${Math.round(v)}%` : max >= 10 ? v.toFixed(1) : `${v.toFixed(1)}/${max}`;
      return { source: r.source, label: RATING_LABELS[r.source] ?? r.source, text };
    });
}

/** `YYYY-MM-DD` dates after today haven't aired. */
export function isUnaired(date?: string, now = new Date()): boolean {
  if (!date) return false;
  const t = Date.parse(date.length === 10 ? `${date}T23:59:59` : date);
  return Number.isFinite(t) && t > now.getTime();
}

export function formatDate(date?: string): string | undefined {
  if (!date) return undefined;
  const t = Date.parse(date.length === 10 ? `${date}T12:00:00` : date);
  if (!Number.isFinite(t)) return undefined;
  return new Date(t).toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  });
}

/** Regular seasons first (specials last), as the native season picker orders them. */
export function orderedSeasons(seasons: Season[]): Season[] {
  return [...seasons]
    .filter((s) => s.episodes.length > 0)
    .sort((a, b) => (a.number === 0 ? 1 : b.number === 0 ? -1 : a.number - b.number));
}

/** Aired regular-season episodes (the watch-state rule: none / partial / complete). */
export function airedRegularEpisodes(seasons: Season[], now = new Date()): Episode[] {
  return seasons
    .filter((s) => s.number > 0)
    .flatMap((s) => s.episodes)
    .filter((e) => !isUnaired(e.airedDate, now));
}

export function watchState(
  episodes: Episode[],
  isWatched: (e: Episode) => boolean,
): 'none' | 'partial' | 'complete' {
  const n = episodes.reduce((acc, e) => acc + (isWatched(e) ? 1 : 0), 0);
  if (n === 0) return 'none';
  return n === episodes.length ? 'complete' : 'partial';
}

/** "1h 2m left" for a resume point. */
export function remainingLabel(position: number, duration: number): string | undefined {
  if (!(duration > 0) || position >= duration) return undefined;
  const left = Math.round((duration - position) / 60);
  return left > 0 ? `${formatRuntime(left)} left` : undefined;
}

/** "21.1 GB". */
export function formatBytes(bytes: number): string | undefined {
  if (!(bytes > 0)) return undefined;
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = bytes;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}
