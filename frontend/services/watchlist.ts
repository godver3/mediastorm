import type { Title, WatchlistItem } from './api';

export type WatchlistMappedTitle = Title & {
  uniqueKey: string;
  addedAt?: string;
  genres?: string[];
  runtimeMinutes?: number;
};

export function mapWatchlistToTitles(
  items: WatchlistItem[],
  cachedYears?: Map<string, number>,
  cachedMetadata?: Map<string, { genres?: string[]; runtimeMinutes?: number }>,
): WatchlistMappedTitle[] {
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
    const cached = cachedMetadata?.get(item.id);
    const title: Title = {
      id: item.id,
      name: item.name,
      overview: item.overview ?? '',
      year: item.year && item.year > 0 ? item.year : (cachedYears?.get(item.id) ?? 0),
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

    return {
      ...title,
      uniqueKey: `${item.mediaType}:${item.id}`,
      addedAt: item.addedAt,
      genres: item.genres ?? cached?.genres,
      runtimeMinutes: item.runtimeMinutes ?? cached?.runtimeMinutes,
    };
  });
}
