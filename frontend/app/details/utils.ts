/**
 * Utility functions for the details screen
 */

export const formatFileSize = (bytes?: number) => {
  if (!bytes || Number.isNaN(bytes)) {
    return 'Unknown size';
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let size = bytes;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex++;
  }
  return `${size.toFixed(unitIndex === 0 ? 0 : size >= 10 ? 0 : 1)} ${units[unitIndex]}`;
};

export const formatPublishDate = (iso?: string) => {
  if (!iso) {
    return '';
  }
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) {
    return '';
  }
  return parsed.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  });
};

export const padNumber = (value: number) => value.toString().padStart(2, '0');

/**
 * Get the display label for a season.
 * Returns "Specials" for season 0 (unless it has a custom name), the season name if available
 * and not a generic "Season X" pattern, or "Season X" otherwise.
 */
export const getSeasonLabel = (seasonNumber: number, seasonName?: string | null): string => {
  // Check if the name is a generic "Season X" pattern (which we want to override)
  const isGenericName = seasonName && /^Season \d+$/i.test(seasonName);

  // For season 0, show "Specials" unless there's a custom (non-generic) name
  if (seasonNumber === 0) {
    return seasonName && !isGenericName ? seasonName : 'Specials';
  }

  // Use custom name if available and not generic, otherwise "Season X"
  return seasonName && !isGenericName ? seasonName : `Season ${seasonNumber}`;
};

export const buildSeasonQuery = (title: string, seasonNumber: number) => {
  const trimmed = title.trim();
  if (!trimmed) {
    return '';
  }
  return `${trimmed} S${padNumber(seasonNumber)}`;
};

export const buildEpisodeQuery = (title: string, seasonNumber: number, episodeNumber: number) => {
  const base = buildSeasonQuery(title, seasonNumber);
  if (!base) {
    return '';
  }
  return `${base}E${padNumber(episodeNumber)}`;
};

export const episodesMatch = (a?: any, b?: any) => {
  if (!a || !b) {
    return false;
  }
  if (a.id && b.id) {
    return a.id === b.id;
  }
  return a.seasonNumber === b.seasonNumber && a.episodeNumber === b.episodeNumber;
};

export const getResultKey = (result: any) =>
  result.guid || result.downloadUrl || result.link || `${result.indexer}:${result.title}`;

/** How many days out an unreleased episode can be and still appear in Continue Watching */
const COMING_SOON_WINDOW_DAYS = 7;

/**
 * Check if an unreleased episode is "coming soon" (within the display window).
 * Returns true only when the air date is in the future AND within COMING_SOON_WINDOW_DAYS.
 * Episodes with no air date or beyond the window return false (should be hidden).
 */
export const isEpisodeComingSoon = (airedDate?: string, airedDateTimeUTC?: string): boolean => {
  if (!airedDate) {
    return false; // No date — don't show
  }

  try {
    // If UTC timestamp available, use precise comparison
    if (airedDateTimeUTC) {
      const airDateTime = new Date(airedDateTimeUTC);
      if (!isNaN(airDateTime.getTime())) {
        const now = new Date();
        if (airDateTime <= now) {
          return false; // Already aired
        }
        const cutoff = new Date(now);
        cutoff.setDate(cutoff.getDate() + COMING_SOON_WINDOW_DAYS);
        return airDateTime <= cutoff;
      }
    }

    // Fallback to date-only logic
    const airDate = new Date(airedDate + 'T00:00:00');
    if (isNaN(airDate.getTime())) {
      return false;
    }

    const today = new Date();
    today.setHours(0, 0, 0, 0);

    if (airDate <= today) {
      return false; // Already aired
    }

    const cutoff = new Date(today);
    cutoff.setDate(cutoff.getDate() + COMING_SOON_WINDOW_DAYS);

    return airDate <= cutoff;
  } catch {
    return false;
  }
};

/**
 * Format a compact airtime label for the coming-soon badge.
 * Uses the UTC timestamp when available (displayed in local time), otherwise date only.
 * Examples: "Today 7:00 PM", "Tmrw 3:30 PM", "Wed 9:00 PM", "Feb 26"
 */
export const formatComingSoonLabel = (airedDate?: string, airedDateTimeUTC?: string): string => {
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const tomorrow = new Date(today);
  tomorrow.setDate(tomorrow.getDate() + 1);

  if (airedDateTimeUTC) {
    const dt = new Date(airedDateTimeUTC);
    if (!isNaN(dt.getTime())) {
      const timeStr = dt.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
      const dtDay = new Date(dt.getFullYear(), dt.getMonth(), dt.getDate());
      if (dtDay.getTime() === today.getTime()) return `Today ${timeStr}`;
      if (dtDay.getTime() === tomorrow.getTime()) return `Tmrw ${timeStr}`;
      const dayName = dt.toLocaleDateString([], { weekday: 'short' });
      return `${dayName} ${timeStr}`;
    }
  }

  if (airedDate) {
    const d = new Date(airedDate + 'T00:00:00');
    if (!isNaN(d.getTime())) {
      const dDay = new Date(d.getFullYear(), d.getMonth(), d.getDate());
      if (dDay.getTime() === today.getTime()) return 'Today';
      if (dDay.getTime() === tomorrow.getTime()) return 'Tmrw';
      return d.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
    }
  }

  return 'Soon';
};

/**
 * Check if an episode hasn't aired yet based on its air date.
 * Returns true if:
 * - The episode has no air date (assumed unreleased)
 * - The episode's air date is in the future
 */
export const isEpisodeUnreleased = (airedDate?: string, airedDateTimeUTC?: string): boolean => {
  if (!airedDate) {
    return true;
  }

  try {
    // If UTC timestamp available, use precise comparison
    if (airedDateTimeUTC) {
      const airDateTime = new Date(airedDateTimeUTC);
      if (!isNaN(airDateTime.getTime())) {
        return airDateTime > new Date();
      }
    }

    // Fallback to date-only logic
    const airDate = new Date(airedDate + 'T00:00:00');
    if (isNaN(airDate.getTime())) {
      return true; // Invalid date, assume unreleased
    }

    const today = new Date();
    today.setHours(0, 0, 0, 0); // Compare dates only, not times

    return airDate > today;
  } catch {
    return true; // Error parsing, assume unreleased
  }
};

/**
 * Check if a movie hasn't been released for home viewing yet.
 * Returns true if neither digital nor physical release has happened.
 * Also considers theatrical release > 12 months ago as released.
 */
export const isMovieUnreleased = (
  homeRelease?: { date?: string; released?: boolean },
  theatricalRelease?: { date?: string; released?: boolean },
): boolean => {
  // If home release flag is explicitly set, use it
  if (homeRelease?.released === true) {
    return false;
  }

  // Check home release date
  if (homeRelease?.date) {
    try {
      const releaseDate = new Date(homeRelease.date);
      if (!isNaN(releaseDate.getTime())) {
        const today = new Date();
        today.setHours(0, 0, 0, 0);
        if (releaseDate <= today) {
          return false; // Home release date has passed
        }
      }
    } catch {
      // Continue to other checks
    }
  }

  // Check if theatrical release was more than 12 months ago
  if (theatricalRelease?.date) {
    try {
      const theatricalDate = new Date(theatricalRelease.date);
      if (!isNaN(theatricalDate.getTime())) {
        const twelveMonthsAgo = new Date();
        twelveMonthsAgo.setMonth(twelveMonthsAgo.getMonth() - 12);
        twelveMonthsAgo.setHours(0, 0, 0, 0);
        if (theatricalDate <= twelveMonthsAgo) {
          return false; // Theatrical release was > 12 months ago, assume home release available
        }
      }
    } catch {
      // Continue
    }
  }

  // No home release info and theatrical < 12 months ago (or no theatrical), assume unreleased
  return true;
};

/**
 * Format a user-friendly message for unreleased episodes when no search results are found.
 */
export const formatUnreleasedMessage = (episodeLabel: string, airedDate?: string): string => {
  if (!airedDate) {
    return `${episodeLabel} hasn't aired yet. No early results found.`;
  }

  try {
    const airDate = new Date(airedDate + 'T00:00:00');
    if (isNaN(airDate.getTime())) {
      return `${episodeLabel} hasn't aired yet. No early results found.`;
    }

    const formatted = airDate.toLocaleDateString(undefined, {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
    });

    return `${episodeLabel} hasn't aired yet. No early results found. Airs ${formatted}.`;
  } catch {
    return `${episodeLabel} hasn't aired yet. No early results found.`;
  }
};

export const toStringParam = (value: unknown): string => {
  if (Array.isArray(value)) {
    return value[0] ?? '';
  }
  if (value === undefined || value === null) {
    return '';
  }
  return String(value);
};
