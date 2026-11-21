/**
 * Shared title model used by every list/shelf/search/details API
 * (mirrors native-tvos StrmrTV/API/TitleModels.swift `Title` and Models.swift `ImageRef`).
 * Extend here rather than redefining per page; keep optional fields optional.
 */
export interface ImageRef {
  url: string;
}

export interface Rating {
  source: string;
  value?: number;
  /** Scale of `value` (10 for IMDb, 100 for Rotten Tomatoes, ...). */
  max?: number;
  score?: string;
  url?: string;
}

export interface Title {
  id: string;
  name: string;
  overview?: string;
  year?: number;
  poster?: ImageRef;
  textPoster?: ImageRef;
  backdrop?: ImageRef;
  backdrops?: ImageRef[];
  logo?: ImageRef;
  /** 'movie' | 'series' */
  mediaType: string;
  imdbId?: string;
  tmdbId?: number;
  tvdbId?: number;
  certification?: string;
  runtimeMinutes?: number;
  genres?: string[];
  status?: string;
  releaseDate?: string;
  availability?: string;
  watchState?: 'none' | 'partial' | 'complete' | string;
  unwatchedCount?: number;
  ratings?: Rating[];
  cardSubtitle?: string;
  cardImage?: ImageRef;
  forceTitleOverlay?: boolean;
  /** Top-billed cast (details only). */
  credits?: { cast: CastMember[] };
  /** Movies: the collection the movie belongs to (details only). */
  collection?: { id: number; name: string };
}

/** One cast member (`models.CastMember`); `id` is the TMDB person id. */
export interface CastMember {
  id: number;
  name: string;
  character?: string;
  profileUrl?: string;
}
