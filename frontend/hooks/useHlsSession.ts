/**
 * useHlsSession - Manages HLS session lifecycle for video playback
 *
 * Handles:
 * - Session creation (cold start)
 * - Warm start (resume from offset)
 * - Seeking within sessions
 * - Audio/subtitle track changes
 * - Keepalive pings with offset validation
 * - Error recovery
 * - Recreation flag tracking
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Platform } from 'react-native';
import { apiService } from '@/services/api';

export interface HlsSessionOptions {
  /** Source path for content (webdav path or URL) */
  sourcePath: string;
  /** Initial playlist URL (if session already created by details screen) */
  initialPlaylistUrl?: string;
  /** Initial start offset for resume */
  initialStartOffset?: number;
  /** Whether content has Dolby Vision */
  hasDolbyVision?: boolean;
  /** Dolby Vision profile string */
  dolbyVisionProfile?: string;
  /** Whether content has HDR10 */
  hasHDR10?: boolean;
  /** Whether to force AAC audio */
  forceAAC?: boolean;
  /** Selected audio track index */
  audioTrackIndex?: number;
  /** Selected subtitle track index */
  subtitleTrackIndex?: number;
  /** User profile ID */
  profileId?: string;
  /** User profile name */
  profileName?: string;
  /** Callback when playback offset needs correction */
  onOffsetCorrection?: (serverOffset: number) => void;
  /** Callback when session is created/recreated */
  onSessionCreated?: (response: HlsSessionResponse) => void;
  /** Callback when fatal error occurs */
  onFatalError?: (error: string) => void;
}

export interface HlsSessionResponse {
  sessionId: string;
  playlistUrl: string;
  duration?: number;
  startOffset?: number;
  actualStartOffset?: number;
}

export interface HlsSessionState {
  /** Current playlist URL */
  playlistUrl: string | null;
  /** Current session ID */
  sessionId: string | null;
  /** Session status */
  status: 'idle' | 'creating' | 'ready' | 'seeking' | 'error';
  /** Error message if any */
  error: string | null;
  /** Requested start offset */
  requestedStartOffset: number;
  /** Actual start offset from session (keyframe-aligned) */
  actualStartOffset: number;
  /** Delta between actual keyframe and requested position (negative = keyframe is earlier) */
  keyframeDelta: number;
  /** Session duration */
  duration: number | null;
  /** Whether we're currently recreating the session (track change, seek) */
  isRecreating: boolean;
}

export interface HlsSessionRefs {
  /** Current session ID ref for synchronous access */
  sessionIdRef: React.MutableRefObject<string | null>;
  /** Session buffer end position ref */
  sessionBufferEndRef: React.MutableRefObject<number>;
  /** Whether currently recreating session ref */
  isRecreatingRef: React.MutableRefObject<boolean>;
  /** Whether to skip track preferences ref */
  skipTrackPreferencesRef: React.MutableRefObject<boolean>;
  /** Pending seek position ref */
  pendingSeekRef: React.MutableRefObject<number | null>;
  /** Current audio track ref */
  audioTrackRef: React.MutableRefObject<number | undefined>;
  /** Current subtitle track ref */
  subtitleTrackRef: React.MutableRefObject<number | undefined>;
}

export interface HlsSessionActions {
  /** Create or recreate session at a target time */
  createSession: (targetTime: number, options?: {
    audioTrack?: number;
    subtitleTrack?: number;
    trackSwitch?: boolean;
  }) => Promise<HlsSessionResponse | null>;
  /** Seek to a specific time (uses seek endpoint or creates new session) */
  seek: (targetTime: number) => Promise<HlsSessionResponse | null>;
  /** Change audio track (recreates session) */
  changeAudioTrack: (trackIndex: number, currentTime: number) => Promise<HlsSessionResponse | null>;
  /** Change subtitle track (does NOT recreate session - uses sidecar VTT) */
  changeSubtitleTrack: (trackIndex: number) => void;
  /** Send keepalive ping with offset validation */
  keepalive: (currentTime?: number, bufferStart?: number) => Promise<{
    startOffset?: number;
    segmentDuration?: number;
  } | null>;
  /** Get current session status */
  getStatus: () => Promise<{
    status: 'active' | 'completed' | 'error';
    duration: number;
    segmentsCreated: number;
    fatalError?: string;
  } | null>;
  /** Build full playlist URL with auth token */
  buildPlaylistUrl: (playlistPath: string) => string;
  /** Reset session state */
  reset: () => void;
  /** Set the recreating flag */
  setRecreating: (value: boolean) => void;
  /** Set skip track preferences flag */
  setSkipTrackPreferences: (value: boolean) => void;
  /** Update session buffer end */
  updateSessionBufferEnd: (value: number) => void;
  /** Set pending seek */
  setPendingSeek: (value: number | null) => void;
}

const MAX_RETRY_COUNT = 3;

export function useHlsSession(options: HlsSessionOptions): [HlsSessionState, HlsSessionActions, HlsSessionRefs] {
  const {
    sourcePath,
    initialPlaylistUrl,
    initialStartOffset = 0,
    hasDolbyVision,
    dolbyVisionProfile,
    hasHDR10,
    forceAAC,
    audioTrackIndex,
    subtitleTrackIndex,
    profileId,
    profileName,
    onOffsetCorrection,
    onSessionCreated,
    onFatalError,
  } = options;

  // State
  const [state, setState] = useState<HlsSessionState>({
    playlistUrl: initialPlaylistUrl || null,
    sessionId: null,
    status: initialPlaylistUrl ? 'ready' : 'idle',
    error: null,
    requestedStartOffset: initialStartOffset,
    actualStartOffset: initialStartOffset,
    keyframeDelta: 0,
    duration: null,
    isRecreating: false,
  });

  // Refs for mutable state (exposed for synchronous access)
  const sessionIdRef = useRef<string | null>(null);
  const sessionBufferEndRef = useRef<number>(initialStartOffset);
  const isRecreatingRef = useRef(false);
  const skipTrackPreferencesRef = useRef(false);
  const pendingSeekRef = useRef<number | null>(null);
  const audioTrackRef = useRef(audioTrackIndex);
  const subtitleTrackRef = useRef(subtitleTrackIndex);
  const retryCountRef = useRef(0);
  const isSeekingRef = useRef(false);

  // Extract session ID from existing playlist URL
  useEffect(() => {
    if (initialPlaylistUrl) {
      const match = initialPlaylistUrl.match(/\/video\/hls\/([^/]+)\/stream\.m3u8/);
      if (match) {
        sessionIdRef.current = match[1];
        setState((prev) => ({ ...prev, sessionId: match[1] }));
      }
    }
  }, [initialPlaylistUrl]);

  // Build full playlist URL with auth token
  const buildPlaylistUrl = useCallback((playlistPath: string): string => {
    const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
    const authToken = apiService.getAuthToken();
    // Add cache-busting parameter to force player to reload playlist after seek
    // iOS AVPlayer caches HLS playlists and won't reload if URL is identical
    const cacheBuster = `_t=${Date.now()}`;
    if (authToken) {
      return `${baseUrl}${playlistPath}?token=${encodeURIComponent(authToken)}&${cacheBuster}`;
    }
    return `${baseUrl}${playlistPath}?${cacheBuster}`;
  }, []);

  // Create a new HLS session
  const createSession = useCallback(
    async (
      targetTime: number,
      sessionOptions?: { audioTrack?: number; subtitleTrack?: number; trackSwitch?: boolean }
    ): Promise<HlsSessionResponse | null> => {
      if (!sourcePath) {
        console.warn('[useHlsSession] Cannot create session: no source path');
        return null;
      }

      const safeTarget = Math.max(0, Number(targetTime) || 0);
      const trimmedPath = sourcePath.trim();
      if (!trimmedPath) {
        return null;
      }

      setState((prev) => ({ ...prev, status: 'creating', error: null }));

      try {
        const audioTrack = sessionOptions?.audioTrack ?? audioTrackRef.current;
        const subtitleTrack = sessionOptions?.subtitleTrack ?? subtitleTrackRef.current;

        console.log('[useHlsSession] Creating session', {
          path: trimmedPath.substring(0, 50),
          start: safeTarget,
          audioTrack,
          subtitleTrack,
          trackSwitch: sessionOptions?.trackSwitch,
        });

        const response = await apiService.createHlsSession({
          path: trimmedPath,
          dv: hasDolbyVision,
          dvProfile: dolbyVisionProfile,
          hdr: hasHDR10,
          forceAAC,
          start: safeTarget,
          audioTrack,
          subtitleTrack,
          profileId,
          profileName,
          trackSwitch: sessionOptions?.trackSwitch,
        });

        sessionIdRef.current = response.sessionId;
        if (audioTrack !== undefined) audioTrackRef.current = audioTrack;
        if (subtitleTrack !== undefined) subtitleTrackRef.current = subtitleTrack;

        const playlistUrl = buildPlaylistUrl(response.playlistUrl);
        const sessionStart =
          typeof response.startOffset === 'number' && response.startOffset >= 0
            ? response.startOffset
            : safeTarget;
        const actualSessionStart =
          typeof response.actualStartOffset === 'number' && response.actualStartOffset >= 0
            ? response.actualStartOffset
            : sessionStart;
        // keyframeDelta: negative = keyframe is earlier than requested
        const keyframeDelta =
          typeof response.keyframeDelta === 'number'
            ? response.keyframeDelta
            : actualSessionStart - sessionStart;

        // Update buffer end to match session start
        sessionBufferEndRef.current = sessionStart;

        // Calculate pending seek (difference between requested and actual start)
        const pendingSeek = Math.max(0, safeTarget - sessionStart);
        pendingSeekRef.current = pendingSeek > 0.5 ? pendingSeek : null;

        const result: HlsSessionResponse = {
          sessionId: response.sessionId,
          playlistUrl,
          duration: response.duration,
          startOffset: sessionStart,
          actualStartOffset: actualSessionStart,
        };

        setState({
          playlistUrl,
          sessionId: response.sessionId,
          status: 'ready',
          error: null,
          requestedStartOffset: safeTarget,
          actualStartOffset: actualSessionStart,
          keyframeDelta,
          duration: response.duration || null,
          isRecreating: isRecreatingRef.current,
        });

        retryCountRef.current = 0;
        console.log('[useHlsSession] Session created:', {
          sessionId: response.sessionId,
          startOffset: sessionStart,
          actualStartOffset: actualSessionStart,
          keyframeDelta,
          pendingSeek: pendingSeekRef.current,
          duration: response.duration,
        });

        onSessionCreated?.(result);
        return result;
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : String(error);
        console.error('[useHlsSession] Failed to create session:', errorMessage);
        setState((prev) => ({
          ...prev,
          status: 'error',
          error: errorMessage,
          isRecreating: false,
        }));
        isRecreatingRef.current = false;
        return null;
      }
    },
    [sourcePath, hasDolbyVision, dolbyVisionProfile, hasHDR10, forceAAC, profileId, profileName, buildPlaylistUrl, onSessionCreated],
  );

  // Seek to a specific time
  const seek = useCallback(
    async (targetTime: number): Promise<HlsSessionResponse | null> => {
      if (isSeekingRef.current) {
        console.log('[useHlsSession] Seek already in progress, ignoring');
        return null;
      }

      const existingSessionId = sessionIdRef.current;
      isSeekingRef.current = true;
      setState((prev) => ({ ...prev, status: 'seeking' }));

      const safeTarget = Math.max(0, Number(targetTime) || 0);

      try {
        let response: HlsSessionResponse | null = null;

        // If we have an existing session, try to use the seek endpoint
        if (existingSessionId) {
          console.log('[useHlsSession] Seeking within existing session', {
            sessionId: existingSessionId,
            targetTime: safeTarget,
          });

          try {
            const seekResponse = await apiService.seekHlsSession(existingSessionId, safeTarget);
            const playlistUrl = buildPlaylistUrl(seekResponse.playlistUrl);
            const sessionStart =
              typeof seekResponse.startOffset === 'number' && seekResponse.startOffset >= 0
                ? seekResponse.startOffset
                : safeTarget;
            const actualSessionStart =
              typeof seekResponse.actualStartOffset === 'number' && seekResponse.actualStartOffset >= 0
                ? seekResponse.actualStartOffset
                : sessionStart;
            // keyframeDelta: negative = keyframe is earlier than requested
            const keyframeDelta =
              typeof seekResponse.keyframeDelta === 'number'
                ? seekResponse.keyframeDelta
                : actualSessionStart - sessionStart;

            sessionBufferEndRef.current = sessionStart;
            const pendingSeek = Math.max(0, safeTarget - sessionStart);
            pendingSeekRef.current = pendingSeek > 0.5 ? pendingSeek : null;

            response = {
              sessionId: existingSessionId,
              playlistUrl,
              duration: seekResponse.duration,
              startOffset: sessionStart,
              actualStartOffset: actualSessionStart,
            };

            // Update state with keyframeDelta
            setState({
              playlistUrl,
              sessionId: existingSessionId,
              status: 'ready',
              error: null,
              requestedStartOffset: safeTarget,
              actualStartOffset: actualSessionStart,
              keyframeDelta,
              duration: seekResponse.duration || null,
              isRecreating: isRecreatingRef.current,
            });

            console.log('[useHlsSession] Seek completed', { response, keyframeDelta });
          } catch (seekError) {
            console.warn('[useHlsSession] Seek endpoint failed, falling back to new session:', seekError);
          }
        }

        // If seek failed or no existing session, create a new one
        if (!response) {
          isSeekingRef.current = false;
          return createSession(safeTarget);
        }

        // State already set in the try block above for seek success

        isSeekingRef.current = false;
        onSessionCreated?.(response);
        return response;
      } catch (error) {
        isSeekingRef.current = false;
        const errorMessage = error instanceof Error ? error.message : String(error);
        console.error('[useHlsSession] Seek failed:', errorMessage);
        setState((prev) => ({
          ...prev,
          status: 'error',
          error: errorMessage,
          isRecreating: false,
        }));
        isRecreatingRef.current = false;
        return null;
      }
    },
    [createSession, buildPlaylistUrl, onSessionCreated],
  );

  // Change audio track (requires new session)
  const changeAudioTrack = useCallback(
    async (trackIndex: number, currentTime: number): Promise<HlsSessionResponse | null> => {
      console.log('[useHlsSession] Changing audio track to:', trackIndex);
      audioTrackRef.current = trackIndex;
      isRecreatingRef.current = true;
      skipTrackPreferencesRef.current = true;
      setState((prev) => ({ ...prev, isRecreating: true }));
      return createSession(currentTime, {
        audioTrack: trackIndex,
        subtitleTrack: subtitleTrackRef.current,
        trackSwitch: true,
      });
    },
    [createSession],
  );

  // Change subtitle track (does NOT recreate session - uses sidecar VTT)
  const changeSubtitleTrack = useCallback((trackIndex: number): void => {
    console.log('[useHlsSession] Changing subtitle track to:', trackIndex);
    subtitleTrackRef.current = trackIndex;
    // No session recreation needed - SubtitleOverlay will fetch new VTT
  }, []);

  // Send keepalive ping with offset validation
  const keepalive = useCallback(
    async (currentTime?: number, bufferStart?: number): Promise<{
      startOffset?: number;
      actualStartOffset?: number;
      keyframeDelta?: number;
      segmentDuration?: number;
    } | null> => {
      const sessionId = sessionIdRef.current;
      if (!sessionId) return null;

      try {
        const response = await apiService.keepaliveHlsSession(sessionId, currentTime, bufferStart);

        // Update state with keyframeDelta from backend (for subtitle sync)
        if (typeof response.keyframeDelta === 'number') {
          setState((prev) => ({
            ...prev,
            actualStartOffset: response.actualStartOffset ?? prev.actualStartOffset,
            keyframeDelta: response.keyframeDelta,
          }));
        }

        // Validate playback offset matches server's startOffset
        if (response.startOffset !== undefined && onOffsetCorrection) {
          const offsetDelta = Math.abs(response.startOffset - sessionBufferEndRef.current);
          if (offsetDelta > 0.5) {
            console.warn('[useHlsSession] Keepalive: playback offset mismatch, correcting', {
              serverStartOffset: response.startOffset,
              serverActualStartOffset: response.actualStartOffset,
              serverKeyframeDelta: response.keyframeDelta,
              clientBufferEnd: sessionBufferEndRef.current,
              delta: offsetDelta,
            });
            onOffsetCorrection(response.startOffset);
          }
        }

        return {
          startOffset: response.startOffset,
          actualStartOffset: response.actualStartOffset,
          keyframeDelta: response.keyframeDelta,
          segmentDuration: response.segmentDuration,
        };
      } catch (error) {
        console.warn('[useHlsSession] Keepalive failed:', error);
        return null;
      }
    },
    [onOffsetCorrection],
  );

  // Get session status
  const getStatus = useCallback(async () => {
    const sessionId = sessionIdRef.current;
    if (!sessionId) return null;

    try {
      const status = await apiService.getHlsSessionStatus(sessionId);

      // Check for fatal errors
      if (status.status === 'error' && status.fatalError) {
        onFatalError?.(status.fatalError);
      }

      return {
        status: status.status,
        duration: status.duration || 0,
        segmentsCreated: status.segmentsCreated,
        fatalError: status.fatalError,
      };
    } catch (error) {
      console.warn('[useHlsSession] Get status failed:', error);
      return null;
    }
  }, [onFatalError]);

  // Reset session state
  const reset = useCallback((): void => {
    sessionIdRef.current = null;
    retryCountRef.current = 0;
    isSeekingRef.current = false;
    isRecreatingRef.current = false;
    skipTrackPreferencesRef.current = false;
    pendingSeekRef.current = null;
    sessionBufferEndRef.current = 0;
    setState({
      playlistUrl: null,
      sessionId: null,
      status: 'idle',
      error: null,
      requestedStartOffset: 0,
      actualStartOffset: 0,
      keyframeDelta: 0,
      duration: null,
      isRecreating: false,
    });
  }, []);

  // Helper setters for refs
  const setRecreating = useCallback((value: boolean): void => {
    isRecreatingRef.current = value;
    setState((prev) => ({ ...prev, isRecreating: value }));
  }, []);

  const setSkipTrackPreferences = useCallback((value: boolean): void => {
    skipTrackPreferencesRef.current = value;
  }, []);

  const updateSessionBufferEnd = useCallback((value: number): void => {
    sessionBufferEndRef.current = value;
  }, []);

  const setPendingSeek = useCallback((value: number | null): void => {
    pendingSeekRef.current = value;
  }, []);

  // Expose refs for synchronous access in player.tsx
  const refs: HlsSessionRefs = useMemo(
    () => ({
      sessionIdRef,
      sessionBufferEndRef,
      isRecreatingRef,
      skipTrackPreferencesRef,
      pendingSeekRef,
      audioTrackRef,
      subtitleTrackRef,
    }),
    [],
  );

  const actions: HlsSessionActions = useMemo(
    () => ({
      createSession,
      seek,
      changeAudioTrack,
      changeSubtitleTrack,
      keepalive,
      getStatus,
      buildPlaylistUrl,
      reset,
      setRecreating,
      setSkipTrackPreferences,
      updateSessionBufferEnd,
      setPendingSeek,
    }),
    [
      createSession,
      seek,
      changeAudioTrack,
      changeSubtitleTrack,
      keepalive,
      getStatus,
      buildPlaylistUrl,
      reset,
      setRecreating,
      setSkipTrackPreferences,
      updateSessionBufferEnd,
      setPendingSeek,
    ],
  );

  return [state, actions, refs];
}

export default useHlsSession;
