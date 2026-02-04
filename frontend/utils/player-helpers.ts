import type { AudioStreamMetadata, SubtitleStreamMetadata } from '@/services/api';

export type ConsoleLevel = 'log' | 'warn' | 'error';

export interface DebugLogEntry {
  id: number;
  level: ConsoleLevel;
  message: string;
  timestamp: string;
}

export type TrackOption = {
  id: string;
  label: string;
  description?: string;
};

export interface PlayerParams extends Record<string, any> {
  movie: string;
  headerImage: string;
  title?: string;
  seriesTitle?: string; // Clean series title without episode code (for metadata lookups)
  debugLogs?: string;
  preferSystemPlayer?: string;
  mediaType?: string;
  tvgId?: string; // EPG channel ID for live TV
  year?: string;
  seasonNumber?: string;
  episodeNumber?: string;
  episodeName?: string;
  durationHint?: string;
  sourcePath?: string;
  displayName?: string;
  releaseName?: string;
  dv?: string;
  dvProfile?: string;
  hdr10?: string;
  forceAAC?: string;
  startOffset?: string;
  actualStartOffset?: string; // Keyframe-aligned start time for subtitle sync
  titleId?: string;
  imdbId?: string;
  tvdbId?: string;
  preExtractedSubtitles?: string; // JSON stringified SubtitleSessionInfo[]
  subtitleDebug?: string; // Enable subtitle sync debug overlay
  shuffleMode?: string; // Random episode playback mode
  preselectedAudioTrack?: string; // Track index already baked into HLS session by prequeue (relative index for native player)
  preselectedSubtitleTrack?: string; // Track index already baked into HLS session by prequeue (relative index for native player)
  passthroughName?: string; // AIOStreams passthrough format: raw display name
  passthroughDescription?: string; // AIOStreams passthrough format: raw description
  useNativePlayer?: string; // Use NativePlayer (KSPlayer/MPV) instead of HLS
  hdrHint?: string; // HDR content type hint: 'HDR10' | 'DolbyVision' | 'HLG'
}

export const parseBooleanParam = (value?: string | string[]): boolean => {
  const raw = Array.isArray(value) ? value[0] : value;
  if (!raw) {
    return false;
  }

  const normalized = raw.toString().trim().toLowerCase();
  return normalized === '1' || normalized === 'true' || normalized === 'yes' || normalized === 'on';
};

export const SUBTITLE_OFF_OPTION: TrackOption = { id: 'off', label: 'Off' };

export const toTitleCase = (value?: string | null): string | undefined => {
  if (!value) {
    return undefined;
  }
  const trimmed = value.trim();
  if (!trimmed) {
    return undefined;
  }
  return trimmed
    .toLowerCase()
    .split(/\s+/)
    .map((segment) => segment.charAt(0).toUpperCase() + segment.slice(1))
    .join(' ');
};

export const stripEpisodeCodeSuffix = (value?: string | null): string | undefined => {
  if (!value) {
    return undefined;
  }
  const trimmed = value.trim();
  if (!trimmed) {
    return undefined;
  }
  const cleaned = trimmed.replace(/\s*[-–:]\s*S\d{2}E\d{2}$/i, '').trim();
  return cleaned || trimmed;
};

export const formatLanguage = (code?: string | null): string | undefined => {
  if (!code) {
    return undefined;
  }
  const trimmed = code.trim();
  if (!trimmed) {
    return undefined;
  }
  if (trimmed.length <= 3) {
    return trimmed.toUpperCase();
  }
  return toTitleCase(trimmed) ?? trimmed;
};

export const buildAudioTrackOptions = (streams: AudioStreamMetadata[]): TrackOption[] => {
  if (!Array.isArray(streams)) {
    return [];
  }
  return streams.map((stream) => {
    const title = toTitleCase(stream.title);
    const language = formatLanguage(stream.language);
    const channel = stream.channelLayout?.trim() || (stream.channels ? `${stream.channels}ch` : undefined);
    const codec = stream.codecLongName ?? stream.codecName;

    const labelParts: string[] = [];
    if (title) {
      labelParts.push(title);
    }
    if (language) {
      labelParts.push(language);
    }
    if (!labelParts.length) {
      labelParts.push(`Audio ${stream.index}`);
    }

    const descriptionParts: string[] = [];
    if (channel) {
      descriptionParts.push(channel);
    }
    if (codec) {
      descriptionParts.push(codec.toUpperCase());
    }

    return {
      id: String(stream.index),
      label: labelParts.join(' · '),
      description: descriptionParts.length ? descriptionParts.join(' · ') : undefined,
    };
  });
};

export const buildSubtitleTrackOptions = (
  streams: SubtitleStreamMetadata[],
  selectedIndex?: number | null,
): TrackOption[] => {
  const options: TrackOption[] = streams.map((stream) => {
    const title = toTitleCase(stream.title);
    const language = formatLanguage(stream.language);
    const labelParts: string[] = [];
    if (title) {
      labelParts.push(title);
    }
    if (language && !labelParts.includes(language)) {
      labelParts.push(language);
    }
    if (!labelParts.length) {
      labelParts.push(`Subtitle ${stream.index}`);
    }

    const descriptorParts: string[] = [];
    // Check disposition for forced/default flags
    const isForced = stream.isForced ?? (stream.disposition?.forced ?? 0) > 0;
    const isDefault = stream.isDefault ?? (stream.disposition?.default ?? 0) > 0;
    if (isForced) {
      descriptorParts.push('Forced');
    }
    if (isDefault) {
      descriptorParts.push('Default');
    }
    // Mark bitmap subtitles (PGS/HDMV) which can only be rendered by native player
    if (stream.isBitmap) {
      descriptorParts.push('Image');
    }
    const codec = stream.codecLongName ?? stream.codecName;
    if (codec) {
      descriptorParts.push(codec.toUpperCase());
    }

    return {
      id: String(stream.index),
      label: labelParts.join(' · '),
      description: descriptorParts.length ? descriptorParts.join(' · ') : undefined,
    };
  });

  const merged = [SUBTITLE_OFF_OPTION, ...options];
  if (selectedIndex === null || selectedIndex === undefined) {
    return merged;
  }

  // Treat negative indices as "no subtitle" - don't create fake tracks for them
  if (selectedIndex < 0) {
    return merged;
  }

  const hasMatch = merged.some((option) => Number(option.id) === selectedIndex);
  if (!hasMatch) {
    merged.push({ id: String(selectedIndex), label: `Subtitle ${selectedIndex}` });
  }
  return merged;
};

export const resolveSelectedTrackId = (
  options: TrackOption[],
  selectedIndex: number | null | undefined,
  fallbackId: string | null = options[0]?.id ?? null,
): string | null => {
  if (!options.length) {
    return null;
  }
  // Treat negative indices as "use fallback" (e.g., for subtitles, this means "off")
  if (typeof selectedIndex === 'number' && Number.isFinite(selectedIndex)) {
    if (selectedIndex < 0) {
      return fallbackId ?? options[0]?.id ?? null;
    }
    const match = options.find((option) => Number(option.id) === selectedIndex);
    if (match) {
      return match.id;
    }
  }
  return fallbackId ?? options[0]?.id ?? null;
};
