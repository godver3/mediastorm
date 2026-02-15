/**
 * useDetailsData — owns all data fetching, bundle hydration, and derived metadata
 * for the details page (series details, movie details, similar content, ratings, etc.)
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  apiService,
  type ContentPreference,
  type DetailsBundleData,
  type SeriesDetails,
  type Title,
  type Trailer,
} from '@/services/api';

interface UseDetailsDataParams {
  titleId: string;
  title: string;
  isSeries: boolean;
  mediaType: string;
  seriesIdentifier: string;
  yearNumber: number | undefined;
  tmdbIdNumber: number | undefined;
  tvdbIdNumber: number | undefined;
  imdbId: string;
  activeUserId: string | null;
  selectedSeasonNumber: number | undefined;
}

export interface DetailsDataResult {
  // Core data
  seriesDetailsData: SeriesDetails | null;
  movieDetails: Title | null;
  detailsBundle: DetailsBundleData | null;
  bundleReady: boolean;

  // Similar content
  similarContent: Title[];
  similarLoading: boolean;

  // Trailers
  trailers: Trailer[];
  primaryTrailer: Trailer | null;
  trailersLoading: boolean;

  // Content preference
  contentPreference: ContentPreference | null;

  // Progress
  episodeProgressMap: Map<string, number>;
  displayProgress: number | null;
  refreshProgress: () => void;

  // Loading states
  movieDetailsLoading: boolean;
  movieDetailsError: string | null;
  seriesDetailsLoading: boolean;

  // Derived metadata
  credits: Title['credits'] | null;
  ratings: import('@/services/api').Rating[];
  genres: string[];
  certification: string | undefined;
  isMetadataLoadingForSkeleton: boolean;

  // For bundle hydration tracking
  hydratedFromBundle: React.MutableRefObject<{
    seriesDetails: boolean;
    movieDetails: boolean;
    similar: boolean;
    trailers: boolean;
    contentPreference: boolean;
    watchState: boolean;
    playbackProgress: boolean;
  }>;
  bundleTrailerSeasonRef: React.MutableRefObject<number | undefined>;
}

// Rating source display order
const RATING_ORDER: Record<string, number> = {
  imdb: 1,
  tmdb: 2,
  trakt: 3,
  tomatoes: 4,
  audience: 5,
  metacritic: 6,
  letterboxd: 7,
};

export function useDetailsData(params: UseDetailsDataParams): DetailsDataResult {
  const {
    titleId,
    title,
    isSeries,
    mediaType,
    seriesIdentifier,
    yearNumber,
    tmdbIdNumber,
    tvdbIdNumber,
    imdbId,
    activeUserId,
    selectedSeasonNumber,
  } = params;

  // Core data state
  const [seriesDetailsData, setSeriesDetailsData] = useState<SeriesDetails | null>(null);
  const [movieDetails, setMovieDetails] = useState<Title | null>(null);
  const [movieDetailsLoading, setMovieDetailsLoading] = useState(true);
  const [movieDetailsError, setMovieDetailsError] = useState<string | null>(null);
  const [seriesDetailsLoading, setSeriesDetailsLoading] = useState(true);

  // Bundle state
  const [detailsBundle, setDetailsBundle] = useState<DetailsBundleData | null>(null);
  const [bundleReady, setBundleReady] = useState(false);
  const hydratedFromBundle = useRef({
    seriesDetails: false,
    movieDetails: false,
    similar: false,
    trailers: false,
    contentPreference: false,
    watchState: false,
    playbackProgress: false,
  });
  const bundleTrailerSeasonRef = useRef<number | undefined>(undefined);

  // Similar content
  const [similarContent, setSimilarContent] = useState<Title[]>([]);
  const [similarLoading, setSimilarLoading] = useState(true);

  // Trailers
  const [trailers, setTrailers] = useState<Trailer[]>([]);
  const [primaryTrailer, setPrimaryTrailer] = useState<Trailer | null>(null);
  const [trailersLoading, setTrailersLoading] = useState(false);

  // Content preference
  const [contentPreference, setContentPreference] = useState<ContentPreference | null>(null);

  // Progress
  const [episodeProgressMap, setEpisodeProgressMap] = useState<Map<string, number>>(new Map());
  const [displayProgress, setDisplayProgress] = useState<number | null>(null);
  const [progressRefreshKey, setProgressRefreshKey] = useState(0);

  const refreshProgress = useCallback(() => {
    setProgressRefreshKey((k) => k + 1);
  }, []);

  // Derive series title for backdrop
  const seriesDetailsForBackdrop = seriesDetailsData?.title ?? null;

  // Movie details query
  const movieDetailsQuery = useMemo(() => {
    if (isSeries) return null;
    const trimmedTitleId = titleId?.trim();
    const trimmedTitleName = title?.trim();
    const trimmedImdbId = imdbId?.trim();
    const query: {
      tmdbId?: number;
      tvdbId?: number;
      titleId?: string;
      name?: string;
      year?: number;
      imdbId?: string;
    } = {};
    if (tmdbIdNumber) query.tmdbId = tmdbIdNumber;
    if (tvdbIdNumber) query.tvdbId = tvdbIdNumber;
    if (trimmedTitleId) query.titleId = trimmedTitleId;
    if (trimmedTitleName) query.name = trimmedTitleName;
    if (typeof yearNumber === 'number') query.year = yearNumber;
    if (trimmedImdbId) query.imdbId = trimmedImdbId;
    if (Object.keys(query).length === 0) return null;
    return query;
  }, [imdbId, isSeries, title, titleId, tmdbIdNumber, tvdbIdNumber, yearNumber]);

  // === Details Bundle: single-request hydration ===
  useEffect(() => {
    if (!activeUserId) return;
    if (!titleId && !title) return;
    let cancelled = false;
    const bundleType = isSeries ? 'series' : 'movie';
    apiService
      .getDetailsBundleData(activeUserId, {
        type: bundleType,
        titleId: titleId || undefined,
        name: title || undefined,
        year: yearNumber || undefined,
        tvdbId: tvdbIdNumber || undefined,
        tmdbId: tmdbIdNumber || undefined,
        imdbId: imdbId || undefined,
      })
      .then((data) => {
        if (cancelled) return;
        setDetailsBundle(data);
        setBundleReady(true);
      })
      .catch((error) => {
        if (cancelled) return;
        console.log('[details-bundle] fetch failed, falling back to individual requests:', error);
        setDetailsBundle(null);
        setBundleReady(true);
      });
    return () => { cancelled = true; };
  }, [activeUserId, titleId, title, isSeries, yearNumber, tvdbIdNumber, tmdbIdNumber, imdbId]);

  // === Consolidated bundle hydration ===
  useEffect(() => {
    if (!detailsBundle) return;

    if (detailsBundle.movieDetails && !hydratedFromBundle.current.movieDetails) {
      hydratedFromBundle.current.movieDetails = true;
      setMovieDetails(detailsBundle.movieDetails);
      setMovieDetailsLoading(false);
    }
    if (detailsBundle.seriesDetails && !hydratedFromBundle.current.seriesDetails) {
      hydratedFromBundle.current.seriesDetails = true;
      setSeriesDetailsData(detailsBundle.seriesDetails);
      setSeriesDetailsLoading(false);
    }
    if (!hydratedFromBundle.current.similar) {
      hydratedFromBundle.current.similar = true;
      setSimilarContent(detailsBundle.similar);
      setSimilarLoading(false);
    }
    if (detailsBundle.trailers && !hydratedFromBundle.current.trailers) {
      hydratedFromBundle.current.trailers = true;
      bundleTrailerSeasonRef.current = undefined;
      const nextTrailers = detailsBundle.trailers.trailers ?? [];
      setTrailers(nextTrailers);
      setPrimaryTrailer(detailsBundle.trailers.primaryTrailer ?? (nextTrailers.length ? nextTrailers[0] : null));
      setTrailersLoading(false);
    }
    if (!hydratedFromBundle.current.contentPreference) {
      hydratedFromBundle.current.contentPreference = true;
      setContentPreference(detailsBundle.contentPreference);
    }
    if (isSeries && seriesIdentifier && !hydratedFromBundle.current.playbackProgress) {
      hydratedFromBundle.current.playbackProgress = true;
      const progressMap = new Map<string, number>();
      const itemIdPrefix = `${seriesIdentifier}:`;
      for (const progress of detailsBundle.playbackProgress) {
        if (progress.mediaType !== 'episode') continue;
        const matchesSeriesId = progress.seriesId === seriesIdentifier;
        const matchesItemIdPrefix = progress.itemId?.startsWith(itemIdPrefix);
        if (matchesSeriesId || matchesItemIdPrefix) {
          let seasonNum = progress.seasonNumber;
          let episodeNum = progress.episodeNumber;
          if ((!seasonNum || !episodeNum) && progress.itemId) {
            const match = progress.itemId.match(/:S(\d+)E(\d+)$/i);
            if (match) {
              seasonNum = parseInt(match[1], 10);
              episodeNum = parseInt(match[2], 10);
            }
          }
          if (seasonNum && episodeNum) {
            const key = `${seasonNum}-${episodeNum}`;
            if (progress.percentWatched > 5 && progress.percentWatched < 95) {
              progressMap.set(key, Math.round(progress.percentWatched));
            }
          }
        }
      }
      setEpisodeProgressMap(progressMap);
    }
  }, [detailsBundle, isSeries, seriesIdentifier]);

  // Fetch movie details (individual fallback)
  useEffect(() => {
    if (!movieDetailsQuery) {
      setMovieDetails(null);
      setMovieDetailsLoading(false);
      return;
    }
    if (!bundleReady) return;
    if (hydratedFromBundle.current.movieDetails) return;
    let cancelled = false;
    setMovieDetailsLoading(true);
    apiService
      .getMovieDetails(movieDetailsQuery)
      .then((details) => {
        if (cancelled) return;
        setMovieDetails(details);
        setMovieDetailsLoading(false);
      })
      .catch((error) => {
        if (cancelled) return;
        console.warn('[details] movie metadata fetch failed', error);
        setMovieDetails(null);
        setMovieDetailsError(error instanceof Error ? error.message : 'Failed to load details');
        setMovieDetailsLoading(false);
      });
    return () => { cancelled = true; };
  }, [movieDetailsQuery, bundleReady]);

  // Fetch similar content (individual fallback)
  useEffect(() => {
    if (!tmdbIdNumber) {
      setSimilarContent([]);
      setSimilarLoading(false);
      return;
    }
    if (!bundleReady) return;
    if (hydratedFromBundle.current.similar) return;
    let cancelled = false;
    setSimilarLoading(true);
    const fetchMediaType = isSeries ? 'series' : 'movie';
    apiService
      .getSimilarContent(fetchMediaType, tmdbIdNumber)
      .then((titles) => {
        if (cancelled) return;
        setSimilarContent(titles);
        setSimilarLoading(false);
      })
      .catch((error) => {
        if (cancelled) return;
        console.warn('[details] similar content fetch failed', error);
        setSimilarContent([]);
        setSimilarLoading(false);
      });
    return () => { cancelled = true; };
  }, [tmdbIdNumber, isSeries, bundleReady]);

  // Fetch series details (individual fallback)
  useEffect(() => {
    if (!isSeries) {
      setSeriesDetailsData(null);
      setSeriesDetailsLoading(false);
      return;
    }
    const normalizedTitle = title?.trim();
    if (!normalizedTitle && !tvdbIdNumber && !titleId) {
      setSeriesDetailsData(null);
      setSeriesDetailsLoading(false);
      return;
    }
    if (!bundleReady) return;
    if (hydratedFromBundle.current.seriesDetails) return;
    let cancelled = false;
    setSeriesDetailsLoading(true);
    apiService
      .getSeriesDetails({
        tvdbId: tvdbIdNumber || undefined,
        titleId: titleId || undefined,
        name: normalizedTitle || undefined,
        year: yearNumber,
        tmdbId: tmdbIdNumber,
      })
      .then((details) => {
        if (cancelled) return;
        setSeriesDetailsData(details);
        setSeriesDetailsLoading(false);
      })
      .catch((error) => {
        if (cancelled) return;
        console.warn('[details] series metadata fetch failed', error);
        setSeriesDetailsData(null);
        setSeriesDetailsLoading(false);
      });
    return () => { cancelled = true; };
  }, [isSeries, title, titleId, tvdbIdNumber, tmdbIdNumber, yearNumber, bundleReady]);

  // Fetch trailers
  useEffect(() => {
    const shouldAttempt = Boolean(tmdbIdNumber || tvdbIdNumber || titleId || title);
    if (!shouldAttempt) {
      setTrailers([]);
      setPrimaryTrailer(null);
      setTrailersLoading(false);
      return;
    }
    if (!bundleReady && !hydratedFromBundle.current.trailers) return;
    if (hydratedFromBundle.current.trailers) {
      const currentSeason = isSeries && selectedSeasonNumber ? selectedSeasonNumber : undefined;
      if (currentSeason === bundleTrailerSeasonRef.current) return;
      bundleTrailerSeasonRef.current = currentSeason;
    }
    if (isSeries && !selectedSeasonNumber) return;
    let cancelled = false;
    setTrailersLoading(true);
    const seasonNumber = isSeries && selectedSeasonNumber ? selectedSeasonNumber : undefined;
    apiService
      .getTrailers({
        mediaType,
        titleId: titleId || undefined,
        name: title || undefined,
        year: yearNumber,
        tmdbId: tmdbIdNumber,
        tvdbId: tvdbIdNumber,
        imdbId: imdbId || undefined,
        season: seasonNumber,
      })
      .then((response) => {
        if (cancelled) return;
        const nextTrailers = response?.trailers ?? [];
        setTrailers(nextTrailers);
        setPrimaryTrailer(response?.primaryTrailer ?? (nextTrailers.length ? nextTrailers[0] : null));
      })
      .catch((error) => {
        if (cancelled) return;
        setTrailers([]);
        setPrimaryTrailer(null);
      })
      .finally(() => {
        if (!cancelled) setTrailersLoading(false);
      });
    return () => { cancelled = true; };
  }, [imdbId, isSeries, mediaType, selectedSeasonNumber, title, titleId, tmdbIdNumber, tvdbIdNumber, yearNumber, bundleReady]);

  // Fetch content preference (individual fallback)
  useEffect(() => {
    const contentId = isSeries ? seriesIdentifier : titleId;
    if (!activeUserId || !contentId) {
      setContentPreference(null);
      return;
    }
    if (!bundleReady) return;
    if (hydratedFromBundle.current.contentPreference) return;
    let cancelled = false;
    apiService
      .getContentPreference(activeUserId, contentId)
      .then((pref) => {
        if (!cancelled) setContentPreference(pref);
      })
      .catch((error) => {
        if (!cancelled) {
          console.log('Unable to fetch content preference:', error);
          setContentPreference(null);
        }
      });
    return () => { cancelled = true; };
  }, [activeUserId, isSeries, seriesIdentifier, titleId, bundleReady]);

  // Fetch progress for all episodes
  useEffect(() => {
    if (!activeUserId || !isSeries || !seriesIdentifier) {
      setEpisodeProgressMap(new Map());
      return;
    }
    const buildProgressMap = (progressList: import('@/services/api').PlaybackProgress[]) => {
      const progressMap = new Map<string, number>();
      const itemIdPrefix = `${seriesIdentifier}:`;
      for (const progress of progressList) {
        if (progress.mediaType !== 'episode') continue;
        const matchesSeriesId = progress.seriesId === seriesIdentifier;
        const matchesItemIdPrefix = progress.itemId?.startsWith(itemIdPrefix);
        if (matchesSeriesId || matchesItemIdPrefix) {
          let seasonNum = progress.seasonNumber;
          let episodeNum = progress.episodeNumber;
          if ((!seasonNum || !episodeNum) && progress.itemId) {
            const match = progress.itemId.match(/:S(\d+)E(\d+)$/i);
            if (match) {
              seasonNum = parseInt(match[1], 10);
              episodeNum = parseInt(match[2], 10);
            }
          }
          if (seasonNum && episodeNum) {
            const key = `${seasonNum}-${episodeNum}`;
            if (progress.percentWatched > 5 && progress.percentWatched < 95) {
              progressMap.set(key, Math.round(progress.percentWatched));
            }
          }
        }
      }
      return progressMap;
    };
    if (!bundleReady && progressRefreshKey === 0) return;
    if (hydratedFromBundle.current.playbackProgress && progressRefreshKey === 0) return;
    let cancelled = false;
    const fetchAllProgress = async () => {
      try {
        const progressList = await apiService.listPlaybackProgress(activeUserId);
        if (cancelled) return;
        setEpisodeProgressMap(buildProgressMap(progressList));
      } catch (error) {
        if (!cancelled) console.log('Unable to fetch episode progress:', error);
      }
    };
    void fetchAllProgress();
    return () => { cancelled = true; };
  }, [activeUserId, isSeries, seriesIdentifier, bundleReady, progressRefreshKey]);

  // Reset episodes loading state when titleId changes
  useEffect(() => {
    if (isSeries) {
      setSeriesDetailsLoading(true);
    } else {
      setSeriesDetailsLoading(false);
    }
  }, [titleId, isSeries]);

  // Derived metadata
  const credits = useMemo(() => {
    if (isSeries) return seriesDetailsForBackdrop?.credits ?? null;
    return movieDetails?.credits ?? null;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop]);

  const ratings = useMemo(() => {
    const rawRatings = isSeries ? (seriesDetailsForBackdrop?.ratings ?? []) : (movieDetails?.ratings ?? []);
    return [...rawRatings].sort((a, b) => {
      const orderA = RATING_ORDER[a.source] ?? 99;
      const orderB = RATING_ORDER[b.source] ?? 99;
      return orderA - orderB;
    });
  }, [isSeries, movieDetails, seriesDetailsForBackdrop]);

  const genres = useMemo(() => {
    const rawGenres = isSeries ? (seriesDetailsForBackdrop?.genres ?? []) : (movieDetails?.genres ?? []);
    return rawGenres.slice(0, 3);
  }, [isSeries, movieDetails, seriesDetailsForBackdrop]);

  const certification = useMemo(() => {
    return isSeries ? seriesDetailsForBackdrop?.certification : movieDetails?.certification;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop]);

  const isMetadataLoadingForSkeleton = isSeries ? seriesDetailsLoading : movieDetailsLoading;

  return {
    seriesDetailsData,
    movieDetails,
    detailsBundle,
    bundleReady,
    similarContent,
    similarLoading,
    trailers,
    primaryTrailer,
    trailersLoading,
    contentPreference,
    episodeProgressMap,
    displayProgress,
    refreshProgress,
    movieDetailsLoading,
    movieDetailsError,
    seriesDetailsLoading,
    credits,
    ratings,
    genres,
    certification,
    isMetadataLoadingForSkeleton,
    hydratedFromBundle,
    bundleTrailerSeasonRef,
  };
}
