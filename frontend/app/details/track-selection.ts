import type { AudioStreamMetadata, SubtitleStreamMetadata } from '@/services/api';

/**
 * Normalizes a language string for comparison.
 */
export const normalizeLanguageForMatching = (lang: string): string => {
  return lang.toLowerCase().trim();
};

/**
 * Checks if a subtitle stream is marked as forced.
 * Checks isForced flag, disposition.forced, or title containing "forced".
 */
export const isStreamForced = (stream: SubtitleStreamMetadata): boolean => {
  if (stream.isForced) return true;
  if ((stream.disposition?.forced ?? 0) > 0) return true;
  if (stream.title?.toLowerCase().includes('forced')) return true;
  return false;
};

/**
 * Checks if a subtitle stream is SDH (Subtitles for Deaf/Hard of Hearing).
 * Checks for SDH, CC, or "hearing impaired" in the title.
 */
export const isStreamSDH = (stream: SubtitleStreamMetadata): boolean => {
  const title = stream.title?.toLowerCase() || '';
  return title.includes('sdh') || title.includes('hearing impaired') || title.includes('cc');
};

/**
 * Finds an audio track matching the preferred language.
 * Returns the track index or null if no match found.
 */
export const findAudioTrackByLanguage = (streams: AudioStreamMetadata[], preferredLanguage: string): number | null => {
  if (!preferredLanguage || !streams?.length) {
    return null;
  }

  const normalizedPref = normalizeLanguageForMatching(preferredLanguage);

  // Try exact match on language code or title
  for (const stream of streams) {
    const language = normalizeLanguageForMatching(stream.language || '');
    const title = normalizeLanguageForMatching(stream.title || '');

    if (language === normalizedPref || title === normalizedPref) {
      return stream.index;
    }
  }

  // Try partial match (e.g., "eng" matches "English")
  for (const stream of streams) {
    const language = normalizeLanguageForMatching(stream.language || '');
    const title = normalizeLanguageForMatching(stream.title || '');

    if (
      language.includes(normalizedPref) ||
      title.includes(normalizedPref) ||
      normalizedPref.includes(language) ||
      normalizedPref.includes(title)
    ) {
      return stream.index;
    }
  }

  return null;
};

/**
 * Finds a subtitle track based on user preferences.
 *
 * Mode behavior:
 * - 'off': Returns null (subtitles disabled)
 * - 'forced-only': Only considers forced subtitle tracks
 * - 'on': Prefers SDH > plain (no title) > any non-forced, with language matching
 *
 * Returns the track index or null if no suitable track found.
 */
export const findSubtitleTrackByPreference = (
  streams: SubtitleStreamMetadata[],
  preferredLanguage: string | undefined,
  mode: 'off' | 'on' | 'forced-only' | undefined,
): number | null => {
  if (!streams?.length || mode === 'off') {
    return null;
  }

  const normalizedPref = preferredLanguage ? normalizeLanguageForMatching(preferredLanguage) : null;

  // Helper to check if stream matches the preferred language
  const matchesLanguage = (stream: SubtitleStreamMetadata): boolean => {
    if (!normalizedPref) return true; // No preference means any language matches
    const language = normalizeLanguageForMatching(stream.language || '');
    const title = normalizeLanguageForMatching(stream.title || '');
    // Exact or partial match
    return (
      language === normalizedPref ||
      title === normalizedPref ||
      language.includes(normalizedPref) ||
      normalizedPref.includes(language)
    );
  };

  // For forced-only mode: only consider forced tracks
  if (mode === 'forced-only') {
    const forcedStreams = streams.filter((s) => isStreamForced(s) && matchesLanguage(s));
    if (forcedStreams.length > 0) {
      return forcedStreams[0].index;
    }
    return null;
  }

  // For 'on' mode: prefer SDH > no title/plain > anything else, exclude forced
  if (mode === 'on') {
    // Get all non-forced streams matching the language
    const nonForcedMatches = streams.filter((s) => !isStreamForced(s) && matchesLanguage(s));

    if (nonForcedMatches.length > 0) {
      // Priority 1: SDH subtitles
      const sdhMatch = nonForcedMatches.find((s) => isStreamSDH(s));
      if (sdhMatch) {
        return sdhMatch.index;
      }

      // Priority 2: No title (plain/full subtitles)
      const plainMatch = nonForcedMatches.find((s) => !s.title || s.title.trim() === '');
      if (plainMatch) {
        return plainMatch.index;
      }

      // Priority 3: Any non-forced match
      return nonForcedMatches[0].index;
    }

    // Fallback: if no non-forced matches, try any stream matching language (including forced)
    const anyMatch = streams.filter((s) => matchesLanguage(s));
    if (anyMatch.length > 0) {
      return anyMatch[0].index;
    }

    // Last resort: first available stream
    return streams[0].index;
  }

  return null;
};
