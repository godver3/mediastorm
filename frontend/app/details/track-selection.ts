import type { AudioStreamMetadata, SubtitleStreamMetadata } from '@/services/api';

/**
 * Normalizes a language string for comparison.
 */
export const normalizeLanguageForMatching = (lang: string): string => {
  return lang.toLowerCase().trim();
};

/**
 * Compatible audio codecs that can be played without transcoding.
 */
const COMPATIBLE_AUDIO_CODECS = new Set(['aac', 'ac3', 'eac3', 'mp3']);

/**
 * Checks if a codec is compatible (can be played without transcoding).
 */
const isCompatibleAudioCodec = (codec: string): boolean => {
  return COMPATIBLE_AUDIO_CODECS.has(codec.toLowerCase().trim());
};

/**
 * Checks if a codec is TrueHD/MLP (particularly problematic for streaming).
 */
const isTrueHDCodec = (codec: string): boolean => {
  const c = codec.toLowerCase().trim();
  return c === 'truehd' || c === 'mlp';
};

/**
 * Checks if an audio track is a commentary track based on its title.
 */
const isCommentaryTrack = (title: string | undefined): boolean => {
  if (!title) return false;
  const lowerTitle = title.toLowerCase().trim();
  const commentaryIndicators = [
    'commentary',
    "director's commentary",
    'directors commentary',
    'audio commentary',
    'cast commentary',
    'crew commentary',
    'isolated score',
    'music only',
    'score only',
  ];
  return commentaryIndicators.some((indicator) => lowerTitle.includes(indicator));
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
 * Checks if a stream matches the preferred language (exact or partial).
 */
const matchesLanguage = (stream: AudioStreamMetadata, normalizedPref: string): boolean => {
  const language = normalizeLanguageForMatching(stream.language || '');
  const title = normalizeLanguageForMatching(stream.title || '');

  // Exact match
  if (language === normalizedPref || title === normalizedPref) {
    return true;
  }
  // Partial match (skip empty strings to avoid false positives)
  if (language && (language.includes(normalizedPref) || normalizedPref.includes(language))) {
    return true;
  }
  if (title && (title.includes(normalizedPref) || normalizedPref.includes(title))) {
    return true;
  }
  return false;
};

/**
 * Finds an audio track matching the preferred language.
 * Prefers compatible audio codecs (AAC, AC3, etc.) over TrueHD/DTS.
 * Specifically avoids TrueHD/MLP unless it's the only option.
 * Skips commentary tracks unless they are the only option.
 * Returns the track index or null if no match found.
 */
export const findAudioTrackByLanguage = (streams: AudioStreamMetadata[], preferredLanguage: string): number | null => {
  if (!preferredLanguage || !streams?.length) {
    return null;
  }

  const normalizedPref = normalizeLanguageForMatching(preferredLanguage);

  // Pass 1: Compatible codec (AAC, AC3, etc.) matching language, skipping commentary
  for (const stream of streams) {
    if (
      matchesLanguage(stream, normalizedPref) &&
      isCompatibleAudioCodec(stream.codecName) &&
      !isCommentaryTrack(stream.title)
    ) {
      return stream.index;
    }
  }

  // Pass 2: Non-TrueHD codec matching language, skipping commentary
  // TrueHD is particularly problematic for streaming, so prefer DTS over TrueHD
  for (const stream of streams) {
    if (
      matchesLanguage(stream, normalizedPref) &&
      !isTrueHDCodec(stream.codecName) &&
      !isCommentaryTrack(stream.title)
    ) {
      return stream.index;
    }
  }

  // Pass 3: TrueHD/MLP matching language, skipping commentary (only if no other option)
  for (const stream of streams) {
    if (
      matchesLanguage(stream, normalizedPref) &&
      isTrueHDCodec(stream.codecName) &&
      !isCommentaryTrack(stream.title)
    ) {
      return stream.index;
    }
  }

  // Pass 4: Compatible codec matching language, including commentary
  for (const stream of streams) {
    if (matchesLanguage(stream, normalizedPref) && isCompatibleAudioCodec(stream.codecName)) {
      return stream.index;
    }
  }

  // Pass 5: Non-TrueHD codec matching language, including commentary
  for (const stream of streams) {
    if (matchesLanguage(stream, normalizedPref) && !isTrueHDCodec(stream.codecName)) {
      return stream.index;
    }
  }

  // Pass 6: TrueHD/MLP matching language, including commentary (last resort)
  for (const stream of streams) {
    if (matchesLanguage(stream, normalizedPref)) {
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

    // No matching language found - return null to trigger auto-search
    // Don't fall back to a random language the user didn't ask for
    return null;
  }

  return null;
};
