import { useEffect, useRef, useState } from 'react';
import { fetchSegments, type IntroDBResponse } from '@/services/introdb';

export type SegmentType = 'intro' | 'recap' | 'outro';

export interface ActiveSegment {
  type: SegmentType;
  startMs: number;
  endMs: number;
}

interface UseIntroSkipParams {
  imdbId: string | undefined;
  seasonNumber: number | undefined;
  episodeNumber: number | undefined;
  mediaType: string | undefined;
  currentTime: number;
}

interface UseIntroSkipResult {
  activeSegment: ActiveSegment | null;
}

export function useIntroSkip({
  imdbId,
  seasonNumber,
  episodeNumber,
  mediaType,
  currentTime,
}: UseIntroSkipParams): UseIntroSkipResult {
  const [segments, setSegments] = useState<IntroDBResponse | null>(null);
  const dismissedSegmentsRef = useRef<Set<SegmentType>>(new Set());
  const lastActiveTypeRef = useRef<SegmentType | null>(null);

  // Fetch segments when episode params change
  useEffect(() => {
    setSegments(null);
    dismissedSegmentsRef.current = new Set();
    lastActiveTypeRef.current = null;

    if (mediaType !== 'episode' || !imdbId || !seasonNumber || !episodeNumber) {
      return;
    }

    let cancelled = false;
    fetchSegments(imdbId, Number(seasonNumber), Number(episodeNumber)).then((result) => {
      if (!cancelled) {
        setSegments(result);
      }
    });

    return () => {
      cancelled = true;
    };
  }, [imdbId, seasonNumber, episodeNumber, mediaType]);

  // Determine active segment based on currentTime
  const currentTimeMs = currentTime * 1000;
  let activeSegment: ActiveSegment | null = null;

  if (segments) {
    const candidates: { type: SegmentType; segment: NonNullable<IntroDBResponse['intro']> }[] = [];
    if (segments.intro) candidates.push({ type: 'intro', segment: segments.intro });
    if (segments.recap) candidates.push({ type: 'recap', segment: segments.recap });
    if (segments.outro) candidates.push({ type: 'outro', segment: segments.outro });

    for (const { type, segment } of candidates) {
      if (
        segment.start_ms != null &&
        segment.end_ms != null &&
        currentTimeMs >= segment.start_ms &&
        currentTimeMs < segment.end_ms &&
        !dismissedSegmentsRef.current.has(type)
      ) {
        activeSegment = { type, startMs: segment.start_ms, endMs: segment.end_ms };
        break;
      }
    }
  }

  // Track dismissals: if we were showing a segment and now we're past it
  // without the user pressing skip, mark it as dismissed so it doesn't re-show
  // if they seek back into it
  const currentActiveType = activeSegment?.type ?? null;
  if (lastActiveTypeRef.current && lastActiveTypeRef.current !== currentActiveType) {
    dismissedSegmentsRef.current.add(lastActiveTypeRef.current);
  }
  lastActiveTypeRef.current = currentActiveType;

  return { activeSegment };
}
