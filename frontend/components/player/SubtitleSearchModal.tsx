import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ActivityIndicator, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import type { SubtitleSearchResult } from '@/services/api';

/**
 * Calculate similarity score between two release names.
 * Higher score = better match.
 */
function calculateReleaseSimilarity(mediaRelease: string, subtitleRelease: string): number {
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

interface SubtitleSearchModalProps {
  visible: boolean;
  onClose: () => void;
  onSelectSubtitle: (subtitle: SubtitleSearchResult) => void;
  searchResults: SubtitleSearchResult[];
  isLoading: boolean;
  error?: string | null;
  onSearch: (language: string) => void;
  currentLanguage: string;
  /** Release name of the currently playing media for similarity matching */
  mediaReleaseName?: string;
}

const LANGUAGES = [
  { code: 'en', name: 'English' },
  { code: 'es', name: 'Spanish' },
  { code: 'fr', name: 'French' },
  { code: 'de', name: 'German' },
  { code: 'it', name: 'Italian' },
  { code: 'pt', name: 'Portuguese' },
  { code: 'nl', name: 'Dutch' },
  { code: 'pl', name: 'Polish' },
  { code: 'ru', name: 'Russian' },
  { code: 'ja', name: 'Japanese' },
  { code: 'ko', name: 'Korean' },
  { code: 'zh', name: 'Chinese' },
  { code: 'ar', name: 'Arabic' },
  { code: 'he', name: 'Hebrew' },
  { code: 'sv', name: 'Swedish' },
  { code: 'no', name: 'Norwegian' },
  { code: 'da', name: 'Danish' },
  { code: 'fi', name: 'Finnish' },
];

export const SubtitleSearchModal: React.FC<SubtitleSearchModalProps> = ({
  visible,
  onClose,
  onSelectSubtitle,
  searchResults,
  isLoading,
  error,
  onSearch,
  currentLanguage,
  mediaReleaseName,
}) => {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const [selectedLanguage, setSelectedLanguage] = useState(currentLanguage || 'en');
  const scrollViewRef = useRef<ScrollView>(null);

  // Sort results by similarity to media release name
  const sortedResults = useMemo(() => {
    if (!mediaReleaseName || searchResults.length === 0) {
      return searchResults;
    }

    // Calculate similarity for each result and sort
    const withScores = searchResults.map((result) => ({
      result,
      score: calculateReleaseSimilarity(mediaReleaseName, result.release),
    }));

    // Sort by score descending, then by downloads as tiebreaker
    withScores.sort((a, b) => {
      if (b.score !== a.score) return b.score - a.score;
      return (b.result.downloads || 0) - (a.result.downloads || 0);
    });

    return withScores.map((item) => item.result);
  }, [searchResults, mediaReleaseName]);

  // Trigger search when language changes
  useEffect(() => {
    if (visible) {
      onSearch(selectedLanguage);
    }
  }, [visible, selectedLanguage, onSearch]);

  const selectGuardRef = useRef(false);
  const withSelectGuard = useCallback((fn: () => void) => {
    if (!Platform.isTV) {
      fn();
      return;
    }
    if (selectGuardRef.current) {
      return;
    }
    selectGuardRef.current = true;
    try {
      fn();
    } finally {
      setTimeout(() => {
        selectGuardRef.current = false;
      }, 250);
    }
  }, []);

  const handleClose = useCallback(() => {
    withSelectGuard(onClose);
  }, [onClose, withSelectGuard]);

  const handleSelectSubtitle = useCallback(
    (subtitle: SubtitleSearchResult) => {
      withSelectGuard(() => onSelectSubtitle(subtitle));
    },
    [onSelectSubtitle, withSelectGuard],
  );

  const handleLanguageChange = useCallback((langCode: string) => {
    setSelectedLanguage(langCode);
  }, []);

  // Back button handling for TV
  const onCloseRef = useRef(onClose);
  const removeInterceptorRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    if (!Platform.isTV || !visible) {
      if (removeInterceptorRef.current) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
      return;
    }

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      onCloseRef.current();
      return true;
    });

    removeInterceptorRef.current = removeInterceptor;

    return () => {
      if (removeInterceptorRef.current) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
    };
  }, [visible]);

  const currentLanguageName = useMemo(
    () => LANGUAGES.find((l) => l.code === selectedLanguage)?.name || selectedLanguage,
    [selectedLanguage],
  );

  if (!visible) {
    return null;
  }

  const renderLanguageSelector = () => (
    <View style={styles.languageSelector}>
      <Text style={styles.languageLabel}>Language:</Text>
      <ScrollView horizontal showsHorizontalScrollIndicator={false} style={styles.languageScrollView}>
        <SpatialNavigationNode orientation="horizontal">
          {LANGUAGES.map((lang) => {
            const isSelected = lang.code === selectedLanguage;
            return (
              <SpatialNavigationFocusableView
                key={lang.code}
                focusKey={`lang-${lang.code}`}
                onSelect={() => handleLanguageChange(lang.code)}
              >
                {({ isFocused }: { isFocused: boolean }) => (
                  <Pressable
                    onPress={() => handleLanguageChange(lang.code)}
                    style={[
                      styles.languageChip,
                      isSelected && styles.languageChipSelected,
                      isFocused && styles.languageChipFocused,
                    ]}
                    tvParallaxProperties={{ enabled: false }}
                  >
                    <Text
                      style={[
                        styles.languageChipText,
                        isSelected && styles.languageChipTextSelected,
                        isFocused && styles.languageChipTextFocused,
                      ]}
                    >
                      {lang.name}
                    </Text>
                  </Pressable>
                )}
              </SpatialNavigationFocusableView>
            );
          })}
        </SpatialNavigationNode>
      </ScrollView>
    </View>
  );

  const renderResult = (result: SubtitleSearchResult, index: number) => {
    // Use index as key to avoid duplicate key errors (opensubtitles can return duplicate IDs)
    const uniqueKey = `subtitle-${index}`;
    const focusKey = `subtitle-result-${index}`;
    const content = (
      <SpatialNavigationFocusableView focusKey={focusKey} onSelect={() => handleSelectSubtitle(result)}>
        {({ isFocused }: { isFocused: boolean }) => (
          <Pressable
            onPress={() => handleSelectSubtitle(result)}
            style={[styles.resultItem, isFocused && styles.resultItemFocused]}
            tvParallaxProperties={{ enabled: false }}
          >
            <View style={styles.resultHeader}>
              <View style={styles.providerBadge}>
                <Text style={styles.providerText}>{result.provider}</Text>
              </View>
              <Text style={[styles.resultLanguage, isFocused && styles.resultTextFocused]}>{result.language}</Text>
              {result.hearing_impaired && (
                <View style={styles.hiBadge}>
                  <Text style={styles.hiText}>HI</Text>
                </View>
              )}
            </View>
            <Text
              style={[styles.resultRelease, isFocused && styles.resultTextFocused]}
              numberOfLines={2}
              ellipsizeMode="tail"
            >
              {result.release || 'Unknown release'}
            </Text>
            <View style={styles.resultFooter}>
              <Ionicons
                name="download-outline"
                size={14}
                color={isFocused ? theme.colors.text.inverse : theme.colors.text.secondary}
              />
              <Text style={[styles.resultDownloads, isFocused && styles.resultTextFocused]}>
                {result.downloads.toLocaleString()} downloads
              </Text>
            </View>
          </Pressable>
        )}
      </SpatialNavigationFocusableView>
    );

    if (index === 0) {
      return <DefaultFocus key={uniqueKey}>{content}</DefaultFocus>;
    }
    return <React.Fragment key={uniqueKey}>{content}</React.Fragment>;
  };

  return (
    <Modal
      visible={visible}
      animationType="fade"
      transparent
      onRequestClose={handleClose}
      supportedOrientations={['portrait', 'portrait-upside-down', 'landscape', 'landscape-left', 'landscape-right']}
      hardwareAccelerated
    >
      <SpatialNavigationRoot isActive={visible}>
        <View style={styles.overlay}>
          <Pressable style={styles.backdrop} onPress={handleClose} tvParallaxProperties={{ enabled: false }} />
          <View style={styles.modalContainer}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>Search Subtitles</Text>
              <Text style={styles.modalSubtitle}>
                {isLoading
                  ? 'Searching for subtitles...'
                  : error
                    ? error
                    : `Found ${sortedResults.length} subtitles in ${currentLanguageName}`}
              </Text>
            </View>

            {renderLanguageSelector()}

            <SpatialNavigationNode orientation="vertical">
              <ScrollView
                ref={scrollViewRef}
                style={styles.resultsScrollView}
                contentContainerStyle={styles.resultsList}
                scrollEnabled={!Platform.isTV}
              >
                {isLoading ? (
                  <View style={styles.loadingContainer}>
                    <ActivityIndicator size="large" color={theme.colors.accent.primary} />
                    <Text style={styles.loadingText}>Searching for subtitles...</Text>
                  </View>
                ) : error ? (
                  <View style={styles.errorContainer}>
                    <Ionicons name="alert-circle-outline" size={48} color={theme.colors.status.danger} />
                    <Text style={styles.errorText}>{error}</Text>
                  </View>
                ) : sortedResults.length === 0 ? (
                  <View style={styles.emptyContainer}>
                    <Ionicons name="search-outline" size={48} color={theme.colors.text.secondary} />
                    <Text style={styles.emptyText}>No subtitles found</Text>
                    <Text style={styles.emptySubtext}>Try a different language</Text>
                  </View>
                ) : (
                  sortedResults.map((result, index) => renderResult(result, index))
                )}
              </ScrollView>
            </SpatialNavigationNode>

            <View style={styles.modalFooter}>
              <SpatialNavigationFocusableView focusKey="subtitle-search-close" onSelect={handleClose}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <Pressable
                    onPress={handleClose}
                    style={[styles.closeButton, isFocused && styles.closeButtonFocused]}
                    tvParallaxProperties={{ enabled: false }}
                  >
                    <Text style={[styles.closeButtonText, isFocused && styles.closeButtonTextFocused]}>Close</Text>
                  </Pressable>
                )}
              </SpatialNavigationFocusableView>
            </View>
          </View>
        </View>
      </SpatialNavigationRoot>
    </Modal>
  );
};

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    overlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    backdrop: {
      ...StyleSheet.absoluteFillObject,
    },
    modalContainer: {
      width: '85%',
      maxWidth: 800,
      maxHeight: '85%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
    },
    modalHeader: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.lg,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      gap: theme.spacing.xs,
    },
    modalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    modalSubtitle: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    languageSelector: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.md,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.md,
    },
    languageLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontWeight: '600',
    },
    languageScrollView: {
      flex: 1,
    },
    languageChip: {
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      marginRight: theme.spacing.sm,
      borderWidth: 1,
      borderColor: 'transparent',
    },
    languageChipSelected: {
      backgroundColor: theme.colors.accent.primary,
    },
    languageChipFocused: {
      borderColor: theme.colors.text.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    languageChipText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    languageChipTextSelected: {
      color: theme.colors.text.inverse,
      fontWeight: '600',
    },
    languageChipTextFocused: {
      color: theme.colors.text.inverse,
    },
    resultsScrollView: {
      flexGrow: 1,
      flexShrink: 1,
    },
    resultsList: {
      padding: theme.spacing.lg,
      gap: theme.spacing.sm,
    },
    resultItem: {
      padding: theme.spacing.md,
      borderRadius: theme.radius.md,
      backgroundColor: 'rgba(255, 255, 255, 0.06)',
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.xs,
    },
    resultItemFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    resultHeader: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
    },
    providerBadge: {
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: 2,
      borderRadius: theme.radius.sm,
      backgroundColor: 'rgba(255, 255, 255, 0.15)',
    },
    providerText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      fontWeight: '600',
      textTransform: 'uppercase',
      fontSize: 10,
    },
    resultLanguage: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    hiBadge: {
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: 1,
      borderRadius: theme.radius.sm,
      backgroundColor: theme.colors.accent.secondary,
    },
    hiText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.inverse,
      fontWeight: '600',
      fontSize: 10,
    },
    resultRelease: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
    },
    resultTextFocused: {
      color: theme.colors.text.inverse,
    },
    resultFooter: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.xs,
    },
    resultDownloads: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontSize: 12,
    },
    loadingContainer: {
      padding: theme.spacing['3xl'],
      alignItems: 'center',
      justifyContent: 'center',
      gap: theme.spacing.md,
    },
    loadingText: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    errorContainer: {
      padding: theme.spacing['3xl'],
      alignItems: 'center',
      justifyContent: 'center',
      gap: theme.spacing.md,
    },
    errorText: {
      ...theme.typography.body.md,
      color: theme.colors.status.danger,
      textAlign: 'center',
    },
    emptyContainer: {
      padding: theme.spacing['3xl'],
      alignItems: 'center',
      justifyContent: 'center',
      gap: theme.spacing.sm,
    },
    emptyText: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
    },
    emptySubtext: {
      ...theme.typography.body.sm,
      color: theme.colors.text.muted,
    },
    modalFooter: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.lg,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'center',
    },
    closeButton: {
      minWidth: 200,
      paddingHorizontal: theme.spacing['2xl'],
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.surface,
      alignItems: 'center',
    },
    closeButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    closeButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    closeButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
  });
