import { requireNativeViewManager, Platform } from 'expo-modules-core';
import React, { forwardRef, useImperativeHandle, useRef } from 'react';
import { StyleProp, ViewStyle, findNodeHandle, UIManager } from 'react-native';

// Types for track information
export interface Track {
  id: number;
  type: string;
  title: string;
  language: string;
  codec: string;
  selected: boolean;
}

export interface LoadEvent {
  duration: number;
  width: number;
  height: number;
}

export interface ProgressEvent {
  currentTime: number;
  duration: number;
}

export interface TracksEvent {
  audioTracks: Track[];
  subtitleTracks: Track[];
}

export interface ErrorEvent {
  error: string;
}

export interface BufferingEvent {
  buffering: boolean;
}

export interface MpvPlayerSource {
  uri: string;
  headers?: Record<string, string>;
}

export interface MpvPlayerProps {
  source?: MpvPlayerSource;
  paused?: boolean;
  volume?: number;
  rate?: number;
  audioTrack?: number;
  subtitleTrack?: number;
  style?: StyleProp<ViewStyle>;
  onLoad?: (event: { nativeEvent: LoadEvent }) => void;
  onProgress?: (event: { nativeEvent: ProgressEvent }) => void;
  onEnd?: (event: { nativeEvent: { ended: boolean } }) => void;
  onError?: (event: { nativeEvent: ErrorEvent }) => void;
  onTracksChanged?: (event: { nativeEvent: TracksEvent }) => void;
  onBuffering?: (event: { nativeEvent: BufferingEvent }) => void;
}

export interface MpvPlayerRef {
  seek: (time: number) => void;
  setAudioTrack: (trackId: number) => void;
  setSubtitleTrack: (trackId: number) => void;
}

// Only load native view on Android
const NativeMpvPlayerView = Platform.OS === 'android'
  ? requireNativeViewManager('MpvPlayer')
  : null;

export const MpvPlayer = forwardRef<MpvPlayerRef, MpvPlayerProps>((props, ref) => {
  const nativeRef = useRef<any>(null);

  useImperativeHandle(ref, () => ({
    seek: (time: number) => {
      if (nativeRef.current && Platform.OS === 'android') {
        const viewTag = findNodeHandle(nativeRef.current);
        if (viewTag) {
          UIManager.dispatchViewManagerCommand(
            viewTag,
            'seek',
            [time]
          );
        }
      }
    },
    setAudioTrack: (trackId: number) => {
      // Track selection is handled via props
      console.log('[MpvPlayer] setAudioTrack called, use audioTrack prop instead');
    },
    setSubtitleTrack: (trackId: number) => {
      // Track selection is handled via props
      console.log('[MpvPlayer] setSubtitleTrack called, use subtitleTrack prop instead');
    },
  }));

  if (!NativeMpvPlayerView) {
    console.log('[MpvPlayer] Native view not available on this platform');
    return null;
  }

  return (
    <NativeMpvPlayerView
      ref={nativeRef}
      {...props}
    />
  );
});

MpvPlayer.displayName = 'MpvPlayer';

export default MpvPlayer;
