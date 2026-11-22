import type { Title } from '@/api/titles';

/**
 * Search params of the details route (`/details`). Every page links to details
 * through `detailsLink()` so the contract lives in one place. The details page
 * fetches `/api/metadata/{movies|series}/details` with these (titleId, name,
 * year, tmdbId, tvdbId, imdbId, userId) like the native clients do.
 */
export interface DetailsSearch {
  mediaType: 'movie' | 'series';
  titleId: string;
  name: string;
  year?: number;
  tmdbId?: number;
  tvdbId?: number;
  imdbId?: string;
  /** Optional deep link to an episode (series only). */
  season?: number;
  episode?: number;
}

export function detailsSearch(
  title: Pick<Title, 'id' | 'name' | 'mediaType' | 'year' | 'tmdbId' | 'tvdbId' | 'imdbId'>,
  extra?: Pick<DetailsSearch, 'season' | 'episode'>,
): DetailsSearch {
  return {
    mediaType: title.mediaType === 'movie' ? 'movie' : 'series',
    titleId: title.id,
    name: title.name,
    ...(title.year ? { year: title.year } : {}),
    ...(title.tmdbId ? { tmdbId: title.tmdbId } : {}),
    ...(title.tvdbId ? { tvdbId: title.tvdbId } : {}),
    ...(title.imdbId ? { imdbId: title.imdbId } : {}),
    ...extra,
  };
}

/** Props for `<Link {...detailsLink(title)} />` or `navigate(detailsLink(title))`. */
export function detailsLink(
  title: Parameters<typeof detailsSearch>[0],
  extra?: Pick<DetailsSearch, 'season' | 'episode'>,
) {
  return { to: '/details' as const, search: detailsSearch(title, extra) };
}

const num = (v: unknown) => {
  const n = typeof v === 'number' ? v : Number(v);
  return Number.isFinite(n) && n > 0 ? n : undefined;
};

export function validateDetailsSearch(raw: Record<string, unknown>): DetailsSearch {
  return {
    mediaType: raw.mediaType === 'movie' ? 'movie' : 'series',
    titleId: String(raw.titleId ?? ''),
    name: String(raw.name ?? ''),
    year: num(raw.year),
    tmdbId: num(raw.tmdbId),
    tvdbId: num(raw.tvdbId),
    imdbId: typeof raw.imdbId === 'string' ? raw.imdbId : undefined,
    season: num(raw.season),
    episode: num(raw.episode),
  };
}
