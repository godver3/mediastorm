import type { SubtitleSearchResult } from '@/services/api';

/**
 * Calculate similarity score between two release names.
 * Higher score = better match.
 */
export function calculateReleaseSimilarity(mediaRelease: string, subtitleRelease: string): number {
  if (!mediaRelease || !subtitleRelease) return 0;

  // Normalize: lowercase, remove extension, split by common delimiters
  const normalize = (s: string) =>
    s
      .toLowerCase()
      .replace(/\.(mkv|mp4|avi|srt|sub)$/i, '')
      .replace(/[._-]/g, ' ')
      .split(/\s+/)
      .filter((w) => w.length > 1);

  const mediaTokens = new Set(normalize(mediaRelease));
  const subTokens = new Set(normalize(subtitleRelease));

  if (mediaTokens.size === 0 || subTokens.size === 0) return 0;

  // Count matching tokens
  let matches = 0;
  for (const token of mediaTokens) {
    if (subTokens.has(token)) matches++;
  }

  // Weight important tokens more heavily
  const importantPatterns = [
    /^\d{3,4}p$/, // Resolution: 720p, 1080p, 2160p
    /^(bluray|bdrip|brrip|webrip|web-dl|webdl|hdtv|hdrip|dvdrip)$/i, // Source
    /^(x264|x265|h264|h265|hevc|avc|xvid)$/i, // Codec
    /^(dts|ac3|aac|truehd|atmos|dd5|ddp5|eac3)$/i, // Audio
    /^(hdr|hdr10|dv|dolby|vision)$/i, // HDR
  ];

  let bonusScore = 0;
  for (const token of mediaTokens) {
    if (subTokens.has(token)) {
      for (const pattern of importantPatterns) {
        if (pattern.test(token)) {
          bonusScore += 2;
          break;
        }
      }
    }
  }

  // Score: percentage of media tokens matched + bonus for important matches
  return (matches / mediaTokens.size) * 100 + bonusScore;
}

/**
 * Select the best subtitle from search results based on similarity to media release name.
 * Falls back to most downloaded if no release name provided.
 */
export function selectBestSubtitle(results: SubtitleSearchResult[], mediaReleaseName?: string): SubtitleSearchResult {
  if (results.length === 0) {
    throw new Error('No subtitle results to select from');
  }

  if (!mediaReleaseName) {
    // Fall back to most downloaded
    return results.reduce((best, current) => (current.downloads > best.downloads ? current : best), results[0]);
  }

  // Score each result
  const scored = results.map((result) => ({
    result,
    score: calculateReleaseSimilarity(mediaReleaseName, result.release),
  }));

  // Sort by score (desc), then downloads (desc) as tiebreaker
  scored.sort((a, b) => {
    if (b.score !== a.score) return b.score - a.score;
    return b.result.downloads - a.result.downloads;
  });

  return scored[0].result;
}
