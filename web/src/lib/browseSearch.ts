import {
  RATING_SOURCES,
  type BrowseKind,
  type BrowseOptions,
  type BrowseSort,
  type RatingSource,
  type WatchFilter,
} from '@/api/lists';

/** URL search params of a browse grid (watchlist, lists). Everything optional = defaults. */
export interface BrowseSearch {
  sort?: BrowseSort;
  dir?: 'asc' | 'desc';
  type?: BrowseKind;
  status?: Exclude<WatchFilter, 'all'>;
  /** Comma-separated genre facets. */
  genre?: string;
  letter?: string;
  /** Rating sort's source (IMDb when unset). */
  rating?: Exclude<RatingSource, 'imdb'>;
}

const SORTS: BrowseSort[] = ['default', 'added', 'name', 'year', 'duration', 'rating'];
const STATUSES = ['unwatched', 'in_progress', 'watched'] as const;

const str = (v: unknown) => (typeof v === 'string' && v.trim() ? v.trim() : undefined);

export function validateBrowseSearch(raw: Record<string, unknown>): BrowseSearch {
  const sort = SORTS.find((s) => s === raw.sort);
  const status = STATUSES.find((s) => s === raw.status);
  return {
    sort: sort && sort !== 'default' ? sort : undefined,
    dir: raw.dir === 'asc' || raw.dir === 'desc' ? raw.dir : undefined,
    type: raw.type === 'movie' || raw.type === 'series' ? raw.type : undefined,
    status,
    genre: str(raw.genre),
    letter: str(raw.letter)?.slice(0, 1).toUpperCase(),
    rating: RATING_SOURCES.find((r) => r.value !== 'imdb' && r.value === raw.rating)?.value as
      Exclude<RatingSource, 'imdb'> | undefined,
  };
}

export function searchToOptions(s: BrowseSearch): BrowseOptions {
  return {
    sort: s.sort ?? 'default',
    dir: s.dir,
    type: s.type,
    status: s.status ?? 'all',
    genres: s.genre ? s.genre.split(',').filter(Boolean) : undefined,
    letter: s.sort === 'name' ? s.letter : undefined,
    ratingSource: s.sort === 'rating' ? s.rating : undefined,
  };
}
