/**
 * Service for managing playback navigation state
 * Used to coordinate between player and details screens
 */

interface NextEpisodeInfo {
  titleId: string;
  seasonNumber: number;
  episodeNumber: number;
  autoPlay: boolean; // When true, automatically start playback instead of just selecting
  shuffleMode: boolean; // When true, continue picking random episodes
  timestamp: number; // To invalidate stale data
}

let nextEpisodeToShow: NextEpisodeInfo | null = null;

const MAX_AGE_MS = 5000; // Only valid for 5 seconds

export const playbackNavigation = {
  /**
   * Set the next episode to show when returning to details page
   * @param autoPlay - When true, automatically start playback of the episode
   * @param shuffleMode - When true, continue picking random episodes
   */
  setNextEpisode(
    titleId: string,
    seasonNumber: number,
    episodeNumber: number,
    autoPlay: boolean = false,
    shuffleMode: boolean = false,
  ) {
    nextEpisodeToShow = {
      titleId,
      seasonNumber,
      episodeNumber,
      autoPlay,
      shuffleMode,
      timestamp: Date.now(),
    };
  },

  /**
   * Get and clear the next episode to show (if it matches the titleId and is still fresh)
   */
  consumeNextEpisode(
    titleId: string,
  ): { seasonNumber: number; episodeNumber: number; autoPlay: boolean; shuffleMode: boolean } | null {
    if (!nextEpisodeToShow) {
      return null;
    }

    // Check if it matches the current title
    if (nextEpisodeToShow.titleId !== titleId) {
      nextEpisodeToShow = null;
      return null;
    }

    // Check if it's still fresh (not too old)
    const age = Date.now() - nextEpisodeToShow.timestamp;
    if (age > MAX_AGE_MS) {
      nextEpisodeToShow = null;
      return null;
    }

    // Consume and clear
    const result = {
      seasonNumber: nextEpisodeToShow.seasonNumber,
      episodeNumber: nextEpisodeToShow.episodeNumber,
      autoPlay: nextEpisodeToShow.autoPlay,
      shuffleMode: nextEpisodeToShow.shuffleMode,
    };
    nextEpisodeToShow = null;
    return result;
  },

  /**
   * Clear any pending next episode
   */
  clear() {
    nextEpisodeToShow = null;
  },
};
