/**
 * Shared utilities for building progress item IDs and checking resume state.
 * Used by both the details page (usePlayback) and the downloads page.
 */

import { apiService } from '@/services/api';

/**
 * Build the itemId used for progress tracking, matching the pattern in
 * player.tsx:1586 and usePlayback.ts:2276.
 *
 * - Episodes: `${seriesIdentifier}:S${sn}E${en}`
 * - Movies: `titleId`
 */
export function buildItemIdForProgress(params: {
  mediaType: 'movie' | 'episode';
  titleId: string;
  seriesIdentifier?: string;
  seasonNumber?: number;
  episodeNumber?: number;
}): string | null {
  const { mediaType, titleId, seriesIdentifier, seasonNumber, episodeNumber } = params;

  if (mediaType === 'episode' && seriesIdentifier && seasonNumber != null && episodeNumber != null) {
    const sn = String(seasonNumber).padStart(2, '0');
    const en = String(episodeNumber).padStart(2, '0');
    return `${seriesIdentifier}:S${sn}E${en}`;
  }

  if (mediaType === 'movie' && titleId) {
    return titleId;
  }

  return null;
}

/**
 * Check if the user has meaningful progress (5–95%) for the given item.
 * Returns `{ percentWatched, position }` if resumable, else null.
 */
export async function checkResumeProgress(
  userId: string,
  mediaType: 'movie' | 'episode',
  itemId: string,
): Promise<{ percentWatched: number; position: number } | null> {
  try {
    const progress = await apiService.getPlaybackProgress(userId, mediaType, itemId);
    if (progress && progress.percentWatched > 5 && progress.percentWatched < 95) {
      return { percentWatched: progress.percentWatched, position: progress.position };
    }
    return null;
  } catch {
    return null;
  }
}
