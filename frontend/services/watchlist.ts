import type { Title, WatchlistItem } from './api';

export function mapWatchlistToTitles(
  items: WatchlistItem[],
  cachedYears?: Map<string, number>,
): Array<Title & { uniqueKey: string }> {
  if (!items) {
    return [];
  }

  const parseNumeric = (value?: string) => {
    if (!value) {
      return undefined;
    }

    const parsed = Number(value);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
  };

  return items.map((item) => {
    const title: Title = {
      id: item.id,
      name: item.name,
      overview: item.overview ?? '',
      year: item.year && item.year > 0 ? item.year : cachedYears?.get(item.id) ?? 0,
      language: 'en',
      mediaType: item.mediaType,
      poster: item.posterUrl ? { url: item.posterUrl, type: 'poster', width: 0, height: 0 } : undefined,
      backdrop: item.backdropUrl ? { url: item.backdropUrl, type: 'backdrop', width: 0, height: 0 } : undefined,
      imdbId: item.externalIds?.imdb,
      tmdbId: parseNumeric(item.externalIds?.tmdb),
      tvdbId: parseNumeric(item.externalIds?.tvdb),
      popularity: undefined,
      network: undefined,
    };

    return { ...title, uniqueKey: `${item.mediaType}:${item.id}` };
  });
}
