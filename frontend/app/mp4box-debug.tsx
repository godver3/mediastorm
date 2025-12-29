import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import LoadingIndicator from '@/components/LoadingIndicator';
import { apiService } from '@/services/api';
import { DefaultFocus, SpatialNavigationRoot } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { router, Stack } from 'expo-router';
import { useCallback, useMemo, useState } from 'react';
import { Platform, ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';

interface ProbeResult {
  format?: {
    duration?: string;
    size?: string;
    format_name?: string;
  };
  streams?: Array<{
    codec_type?: string;
    codec_name?: string;
    width?: number;
    height?: number;
    color_transfer?: string;
    color_primaries?: string;
  }>;
  novastream_analysis?: {
    hasDolbyVision?: boolean;
    dvProfile?: string;
    hasHDR10?: boolean;
  };
}

interface SessionResult {
  sessionId: string;
  playlistUrl: string;
  duration: number;
  startOffset: number;
  hasDV: boolean;
  hasHDR: boolean;
}

export default function MP4BoxDebugScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);

  const [sourceUrl, setSourceUrl] = useState('');
  const [isProbing, setIsProbing] = useState(false);
  const [isStartingSession, setIsStartingSession] = useState(false);
  const [probeResult, setProbeResult] = useState<ProbeResult | null>(null);
  const [sessionResult, setSessionResult] = useState<SessionResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [logs, setLogs] = useState<string[]>([]);

  const addLog = useCallback((message: string) => {
    const timestamp = new Date().toLocaleTimeString();
    setLogs((prev) => [`[${timestamp}] ${message}`, ...prev].slice(0, 50));
  }, []);

  const getApiBase = useCallback(() => {
    const baseUrl = apiService.getBaseUrl();
    if (!baseUrl) {
      return null;
    }
    return baseUrl;
  }, []);

  const getAuthToken = useCallback(() => {
    return apiService.getAuthToken() ?? '';
  }, []);

  const handleProbe = useCallback(async () => {
    if (!sourceUrl.trim()) {
      setError('Please enter a source URL');
      return;
    }

    const apiBase = getApiBase();
    if (!apiBase) {
      setError('Backend not configured');
      return;
    }

    setIsProbing(true);
    setError(null);
    setProbeResult(null);
    addLog(`Probing: ${sourceUrl}`);

    try {
      const probeUrl = `${apiBase}/video/debug/mp4box/probe?url=${encodeURIComponent(sourceUrl)}&token=${getAuthToken()}`;
      addLog(`Request: ${probeUrl}`);

      const response = await fetch(probeUrl);
      if (!response.ok) {
        throw new Error(`Probe failed: ${response.status} ${response.statusText}`);
      }

      const result = await response.json();
      setProbeResult(result);
      addLog(
        `Probe complete: DV=${result.novastream_analysis?.hasDolbyVision}, HDR10=${result.novastream_analysis?.hasHDR10}`,
      );
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unknown error';
      setError(message);
      addLog(`Error: ${message}`);
    } finally {
      setIsProbing(false);
    }
  }, [sourceUrl, getApiBase, getAuthToken, addLog]);

  const handleStartSession = useCallback(async () => {
    if (!sourceUrl.trim()) {
      setError('Please enter a source URL');
      return;
    }

    const apiBase = getApiBase();
    if (!apiBase) {
      setError('Backend not configured');
      return;
    }

    setIsStartingSession(true);
    setError(null);
    setSessionResult(null);
    addLog(`Starting MP4Box HLS session...`);

    try {
      const hasDV = probeResult?.novastream_analysis?.hasDolbyVision || false;
      const hasHDR = probeResult?.novastream_analysis?.hasHDR10 || false;
      const dvProfile = probeResult?.novastream_analysis?.dvProfile || '';

      const params = new URLSearchParams({
        url: sourceUrl,
        dv: hasDV.toString(),
        hdr: hasHDR.toString(),
        token: getAuthToken(),
      });
      if (dvProfile) {
        params.set('dvProfile', dvProfile);
      }

      const sessionUrl = `${apiBase}/video/debug/mp4box/start?${params.toString()}`;
      addLog(`Request: ${sessionUrl}`);

      const response = await fetch(sessionUrl);
      if (!response.ok) {
        const text = await response.text();
        throw new Error(`Session start failed: ${response.status} - ${text}`);
      }

      const result: SessionResult = await response.json();
      setSessionResult(result);
      addLog(`Session created: ${result.sessionId}`);
      addLog(`Playlist URL: ${result.playlistUrl}`);
      addLog(`Duration: ${result.duration}s, DV=${result.hasDV}, HDR=${result.hasHDR}`);
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unknown error';
      setError(message);
      addLog(`Error: ${message}`);
    } finally {
      setIsStartingSession(false);
    }
  }, [sourceUrl, getApiBase, getAuthToken, probeResult, addLog]);

  const handlePlay = useCallback(() => {
    if (!sessionResult?.playlistUrl) {
      setError('No session available');
      return;
    }

    const apiBase = getApiBase();
    if (!apiBase) {
      setError('Backend not configured');
      return;
    }

    // Build the full playlist URL
    const playlistUrl = `${apiBase.replace('/api', '')}${sessionResult.playlistUrl}?token=${getAuthToken()}`;
    addLog(`Launching player with: ${playlistUrl}`);

    router.push({
      pathname: '/player-debug',
      params: {
        movie: playlistUrl,
        title: 'MP4Box Debug Stream',
        dv: sessionResult.hasDV ? 'true' : 'false',
        durationHint: sessionResult.duration.toString(),
        headerImage: '',
      },
    });
  }, [sessionResult, getApiBase, getAuthToken, addLog]);

  const handleClear = useCallback(() => {
    setSourceUrl('');
    setProbeResult(null);
    setSessionResult(null);
    setError(null);
    setLogs([]);
  }, []);

  const analysisInfo = probeResult?.novastream_analysis;
  const videoStream = probeResult?.streams?.find((s) => s.codec_type === 'video');

  return (
    <>
      <Stack.Screen options={{ headerShown: false }} />
      <SpatialNavigationRoot isActive={Platform.isTV}>
        <FixedSafeAreaView style={styles.safeArea}>
          <View style={styles.container}>
            <View style={styles.header}>
              <Text style={styles.title}>MP4Box Debug Player</Text>
              <Text style={styles.subtitle}>
                Test Dolby Vision / HDR streaming with MP4Box instead of FFmpeg. Enter a direct media URL to probe and
                play.
              </Text>
            </View>

            <View style={styles.inputSection}>
              <Text style={styles.label}>Source URL:</Text>
              <TextInput
                style={styles.input}
                value={sourceUrl}
                onChangeText={setSourceUrl}
                placeholder="https://example.com/video.mkv"
                placeholderTextColor={theme.colors.text.muted}
                autoCapitalize="none"
                autoCorrect={false}
                keyboardType="url"
              />
            </View>

            <View style={styles.buttonRow}>
              <DefaultFocus>
                <FocusablePressable
                  focusKey="probe-btn"
                  text={isProbing ? 'Probing...' : 'Probe Video'}
                  onSelect={handleProbe}
                  disabled={isProbing || !sourceUrl.trim()}
                  style={styles.button}
                />
              </DefaultFocus>
              <FocusablePressable
                focusKey="start-session-btn"
                text={isStartingSession ? 'Starting...' : 'Start MP4Box Session'}
                onSelect={handleStartSession}
                disabled={isStartingSession || !sourceUrl.trim()}
                style={styles.button}
              />
              <FocusablePressable
                focusKey="play-btn"
                text="Play Stream"
                onSelect={handlePlay}
                disabled={!sessionResult?.playlistUrl}
                style={styles.button}
              />
              <FocusablePressable focusKey="clear-btn" text="Clear" onSelect={handleClear} style={styles.button} />
              <FocusablePressable
                focusKey="back-btn"
                text="Back"
                onSelect={() => router.back()}
                style={styles.button}
              />
            </View>

            {error && (
              <View style={styles.errorBanner}>
                <Text style={styles.errorText}>{error}</Text>
              </View>
            )}

            {(isProbing || isStartingSession) && (
              <View style={styles.loadingContainer}>
                <LoadingIndicator />
              </View>
            )}

            <View style={styles.resultsRow}>
              <View style={styles.resultCard}>
                <Text style={styles.cardTitle}>Probe Results</Text>
                {probeResult ? (
                  <ScrollView style={styles.cardContent}>
                    <Text style={styles.resultLabel}>Format:</Text>
                    <Text style={styles.resultValue}>{probeResult.format?.format_name || 'N/A'}</Text>

                    <Text style={styles.resultLabel}>Duration:</Text>
                    <Text style={styles.resultValue}>
                      {probeResult.format?.duration
                        ? `${Math.floor(Number(probeResult.format.duration) / 60)}m ${Math.floor(Number(probeResult.format.duration) % 60)}s`
                        : 'N/A'}
                    </Text>

                    {videoStream && (
                      <>
                        <Text style={styles.resultLabel}>Video:</Text>
                        <Text style={styles.resultValue}>
                          {videoStream.codec_name} {videoStream.width}x{videoStream.height}
                        </Text>
                        <Text style={styles.resultLabel}>Color:</Text>
                        <Text style={styles.resultValue}>
                          {videoStream.color_primaries || 'N/A'} / {videoStream.color_transfer || 'N/A'}
                        </Text>
                      </>
                    )}

                    <Text style={styles.resultLabel}>Dolby Vision:</Text>
                    <Text style={[styles.resultValue, analysisInfo?.hasDolbyVision && styles.hdrActive]}>
                      {analysisInfo?.hasDolbyVision ? `YES (${analysisInfo.dvProfile})` : 'No'}
                    </Text>

                    <Text style={styles.resultLabel}>HDR10:</Text>
                    <Text style={[styles.resultValue, analysisInfo?.hasHDR10 && styles.hdrActive]}>
                      {analysisInfo?.hasHDR10 ? 'YES' : 'No'}
                    </Text>
                  </ScrollView>
                ) : (
                  <Text style={styles.placeholder}>Enter a URL and click &quot;Probe Video&quot; to analyze</Text>
                )}
              </View>

              <View style={styles.resultCard}>
                <Text style={styles.cardTitle}>Session Info</Text>
                {sessionResult ? (
                  <ScrollView style={styles.cardContent}>
                    <Text style={styles.resultLabel}>Session ID:</Text>
                    <Text style={styles.resultValue}>{sessionResult.sessionId}</Text>

                    <Text style={styles.resultLabel}>Playlist URL:</Text>
                    <Text style={styles.resultValue} numberOfLines={2}>
                      {sessionResult.playlistUrl}
                    </Text>

                    <Text style={styles.resultLabel}>Duration:</Text>
                    <Text style={styles.resultValue}>
                      {sessionResult.duration > 0
                        ? `${Math.floor(sessionResult.duration / 60)}m ${Math.floor(sessionResult.duration % 60)}s`
                        : 'Unknown'}
                    </Text>

                    <Text style={styles.resultLabel}>DV/HDR:</Text>
                    <Text style={styles.resultValue}>
                      DV={sessionResult.hasDV ? 'Yes' : 'No'}, HDR={sessionResult.hasHDR ? 'Yes' : 'No'}
                    </Text>
                  </ScrollView>
                ) : (
                  <Text style={styles.placeholder}>Click &quot;Start MP4Box Session&quot; to create HLS stream</Text>
                )}
              </View>
            </View>

            <View style={styles.logSection}>
              <Text style={styles.cardTitle}>Debug Log</Text>
              <ScrollView style={styles.logContainer}>
                {logs.length === 0 ? (
                  <Text style={styles.placeholder}>No logs yet</Text>
                ) : (
                  logs.map((log, index) => (
                    <Text key={index} style={styles.logEntry}>
                      {log}
                    </Text>
                  ))
                )}
              </ScrollView>
            </View>
          </View>
        </FixedSafeAreaView>
      </SpatialNavigationRoot>
    </>
  );
}

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    container: {
      flex: 1,
      padding: theme.spacing.lg,
      gap: theme.spacing.md,
    },
    header: {
      gap: theme.spacing.xs,
    },
    title: {
      fontSize: 28,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    subtitle: {
      fontSize: 14,
      color: theme.colors.text.secondary,
    },
    inputSection: {
      gap: theme.spacing.xs,
    },
    label: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
    },
    input: {
      backgroundColor: theme.colors.background.elevated,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      borderRadius: theme.radius.md,
      padding: theme.spacing.md,
      color: theme.colors.text.primary,
      fontSize: 16,
    },
    buttonRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm,
    },
    button: {
      minWidth: 140,
    },
    errorBanner: {
      backgroundColor: theme.colors.status.danger,
      padding: theme.spacing.sm,
      borderRadius: theme.radius.sm,
    },
    errorText: {
      color: theme.colors.text.inverse,
      textAlign: 'center',
    },
    loadingContainer: {
      alignItems: 'center',
      padding: theme.spacing.md,
    },
    resultsRow: {
      flexDirection: 'row',
      gap: theme.spacing.md,
      flex: 1,
    },
    resultCard: {
      flex: 1,
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.md,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      padding: theme.spacing.md,
    },
    cardTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.sm,
    },
    cardContent: {
      flex: 1,
    },
    resultLabel: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.xs,
    },
    resultValue: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
    },
    hdrActive: {
      color: theme.colors.status.success,
      fontWeight: '600',
    },
    placeholder: {
      ...theme.typography.body.sm,
      color: theme.colors.text.muted,
      fontStyle: 'italic',
    },
    logSection: {
      flex: 1,
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.md,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      padding: theme.spacing.md,
    },
    logContainer: {
      flex: 1,
    },
    logEntry: {
      fontFamily: 'SpaceMono',
      fontSize: 12,
      color: theme.colors.text.secondary,
      paddingVertical: 2,
    },
  });
