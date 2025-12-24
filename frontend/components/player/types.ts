export type VideoImplementation = 'mobile-system' | 'web' | 'vlc' | 'expo' | 'rnv';

export type VideoPlayerHandle = {
  seek: (seconds: number) => void;
  toggleFullscreen?: () => void;
  play?: () => void;
  pause?: () => void;
};

export interface VideoProgressMeta {
  playable?: number;
  seekable?: number;
}

export type TrackInfo = {
  id: number;
  name: string;
};

export interface NowPlayingMetadata {
  title?: string;
  subtitle?: string;
  artist?: string;
  imageUri?: string;
}

export interface VideoPlayerProps {
  movie: string;
  headerImage: string;
  movieTitle?: string;
  paused: boolean;
  controls: boolean;
  onBuffer: (isBuffering: boolean) => void;
  onProgress: (currentTime: number, meta?: VideoProgressMeta) => void;
  onLoad: (duration: number) => void;
  onEnd: () => void;
  onError?: (error: unknown) => void;
  durationHint?: number;
  onInteract?: () => void;
  onTogglePlay?: () => void;
  volume?: number;
  onAutoplayBlocked?: () => void;
  onToggleFullscreen?: () => void;
  onImplementationResolved?: (implementation: VideoImplementation) => void;
  selectedAudioTrackIndex?: number | null;
  selectedSubtitleTrackIndex?: number | null;
  onTracksAvailable?: (audioTracks: TrackInfo[], subtitleTracks: TrackInfo[]) => void;
  /** Called when video dimensions are known (for subtitle positioning relative to video content) */
  onVideoSize?: (width: number, height: number) => void;
  forceExpoPlayer?: boolean;
  forceRnvPlayer?: boolean;
  forceNativeFullscreen?: boolean;
  onNativeFullscreenExit?: () => void;
  mediaType?: string;
  nowPlaying?: NowPlayingMetadata;
  /** Subtitle size scale factor (1.0 = default, used for VLC built-in subtitles) */
  subtitleSize?: number;
}
