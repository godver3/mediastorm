import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ActivityIndicator, BackHandler, FlatList, Modal, Platform, Pressable, StyleSheet, Text, useWindowDimensions, View } from 'react-native';

import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import type { SubtitleSearchResult } from '@/services/api';
import { calculateReleaseSimilarity } from '@/utils/subtitle-helpers';

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
  { code: 'hr', name: 'Croatian' },
  { code: 'sr', name: 'Serbian' },
  { code: 'bs', name: 'Bosnian' },
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
  const { width: screenWidth } = useWindowDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth), [theme, screenWidth]);
  const [selectedLanguage, setSelectedLanguage] = useState(currentLanguage || 'en');

  // Sync selectedLanguage with currentLanguage prop when it changes
  // This handles the case where settings load after the component mounts
  useEffect(() => {
    if (currentLanguage) {
      setSelectedLanguage(currentLanguage);
    }
  }, [currentLanguage]);

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

    // Use both BackHandler (for tvOS menu button) and RemoteControlManager
    const backHandler = BackHandler.addEventListener('hardwareBackPress', () => {
      onCloseRef.current();
      return true;
    });

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      onCloseRef.current();
      return true;
    });

    removeInterceptorRef.current = () => {
      backHandler.remove();
      removeInterceptor();
    };

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

  // Language chip dimensions
  const LANGUAGE_CHIP_WIDTH = 100;
  const LANGUAGE_CHIP_MARGIN = 8;

  // Track focused elements for native TV navigation
  const [focusedLanguageCode, setFocusedLanguageCode] = useState<string | null>(null);
  const [focusedResultIndex, setFocusedResultIndex] = useState<number | null>(null);
  const [isCloseFocused, setIsCloseFocused] = useState(false);
  const languageListRef = useRef<FlatList>(null);
  const resultsListRef = useRef<FlatList>(null);

  // Scroll to focused language chip
  const handleLanguageFocus = useCallback((langCode: string, index: number) => {
    setFocusedLanguageCode(langCode);
    if (Platform.isTV && languageListRef.current) {
      languageListRef.current.scrollToIndex({ index, animated: true, viewPosition: 0.3 });
    }
  }, []);

  // Scroll to focused result
  const handleResultFocus = useCallback((index: number) => {
    setFocusedResultIndex(index);
    if (Platform.isTV && resultsListRef.current) {
      resultsListRef.current.scrollToIndex({ index, animated: true, viewPosition: 0.3 });
    }
  }, []);

  const renderLanguageItem = useCallback(
    ({ item: lang, index }: { item: { code: string; name: string }; index: number }) => {
      const isSelected = lang.code === selectedLanguage;
      const isFocused = focusedLanguageCode === lang.code;
      const shouldHaveInitialFocus = Platform.isTV && lang.code === selectedLanguage;

      return (
        <Pressable
          onPress={() => handleLanguageChange(lang.code)}
          onFocus={() => handleLanguageFocus(lang.code, index)}
          onBlur={() => {
            if (focusedLanguageCode === lang.code) {
              setFocusedLanguageCode(null);
            }
          }}
          style={[
            styles.languageChip,
            { width: LANGUAGE_CHIP_WIDTH, marginRight: LANGUAGE_CHIP_MARGIN },
            isSelected && styles.languageChipSelected,
            isFocused && styles.languageChipFocused,
          ]}
          hasTVPreferredFocus={shouldHaveInitialFocus}
          tvParallaxProperties={{ enabled: false }}
        >
          <Text
            style={[
              styles.languageChipText,
              isSelected && styles.languageChipTextSelected,
              isFocused && styles.languageChipTextFocused,
            ]}
            numberOfLines={1}
          >
            {lang.name}
          </Text>
        </Pressable>
      );
    },
    [selectedLanguage, focusedLanguageCode, handleLanguageChange, handleLanguageFocus, styles],
  );

  const renderResult = useCallback(
    ({ item: result, index }: { item: SubtitleSearchResult; index: number }) => {
      const isFocused = focusedResultIndex === index;

      return (
        <Pressable
          onPress={() => handleSelectSubtitle(result)}
          onFocus={() => handleResultFocus(index)}
          onBlur={() => {
            if (focusedResultIndex === index) {
              setFocusedResultIndex(null);
            }
          }}
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
          <Text style={[styles.resultRelease, isFocused && styles.resultTextFocused]}>
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
      );
    },
    [focusedResultIndex, handleSelectSubtitle, handleResultFocus, styles, theme.colors.text.inverse, theme.colors.text.secondary],
  );

  if (!visible) {
    return null;
  }

  const renderLanguageSelector = () => (
    <View style={styles.languageSelector}>
      <Text style={styles.languageLabel}>Language:</Text>
      <FlatList
        ref={languageListRef}
        data={LANGUAGES}
        renderItem={renderLanguageItem}
        keyExtractor={(item) => item.code}
        horizontal
        showsHorizontalScrollIndicator={false}
        style={styles.languageScrollView}
        contentContainerStyle={styles.languageList}
        onScrollToIndexFailed={() => {}}
      />
    </View>
  );

  const renderResultsList = () => {
    if (isLoading) {
      return (
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="large" color={theme.colors.accent.primary} />
        </View>
      );
    }

    if (error) {
      return (
        <View style={styles.errorContainer}>
          <Ionicons name="alert-circle-outline" size={48} color={theme.colors.status.danger} />
          <Text style={styles.errorText}>{error}</Text>
        </View>
      );
    }

    if (sortedResults.length === 0) {
      return (
        <View style={styles.emptyContainer}>
          <Ionicons name="search-outline" size={48} color={theme.colors.text.secondary} />
          <Text style={styles.emptyText}>No subtitles found</Text>
          <Text style={styles.emptySubtext}>Try a different language</Text>
        </View>
      );
    }

    return (
      <FlatList
        ref={resultsListRef}
        data={sortedResults}
        renderItem={renderResult}
        keyExtractor={(_, index) => `subtitle-${index}`}
        style={styles.resultsScrollView}
        contentContainerStyle={styles.resultsList}
        onScrollToIndexFailed={() => {}}
      />
    );
  };

  // On TV, render as a View (to be placed inside TVControlsModal)
  // On mobile, use Modal for proper presentation
  if (Platform.isTV) {
    return (
      <View style={styles.overlay}>
        {/* Use View instead of Pressable on TV to prevent accidental closes - use Menu button to close */}
        <View style={styles.backdrop} />
        <View style={styles.modalContainer}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Search Subtitles</Text>
            {!isLoading && (
              <Text style={styles.modalSubtitle}>
                {error ? error : `Found ${sortedResults.length} subtitles in ${currentLanguageName}`}
              </Text>
            )}
          </View>

          {renderLanguageSelector()}

          {renderResultsList()}

          <View style={styles.modalFooter}>
            <Pressable
              onPress={handleClose}
              onFocus={() => setIsCloseFocused(true)}
              onBlur={() => setIsCloseFocused(false)}
              style={[styles.closeButton, isCloseFocused && styles.closeButtonFocused]}
              tvParallaxProperties={{ enabled: false }}
            >
              <Text style={[styles.closeButtonText, isCloseFocused && styles.closeButtonTextFocused]}>Close</Text>
            </Pressable>
          </View>
        </View>
      </View>
    );
  }

  return (
    <Modal
      visible={visible}
      animationType="fade"
      transparent
      onRequestClose={handleClose}
      supportedOrientations={['portrait', 'portrait-upside-down', 'landscape', 'landscape-left', 'landscape-right']}
      hardwareAccelerated
    >
      <View style={styles.overlay}>
        <Pressable style={styles.backdrop} onPress={handleClose} tvParallaxProperties={{ enabled: false }} focusable={false} />
        <View style={styles.modalContainer}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Search Subtitles</Text>
            {!isLoading && (
              <Text style={styles.modalSubtitle}>
                {error ? error : `Found ${sortedResults.length} subtitles in ${currentLanguageName}`}
              </Text>
            )}
          </View>

          {renderLanguageSelector()}

          {renderResultsList()}

          <View style={styles.modalFooter}>
            <Pressable
              onPress={handleClose}
              onFocus={() => setIsCloseFocused(true)}
              onBlur={() => setIsCloseFocused(false)}
              style={[styles.closeButton, isCloseFocused && styles.closeButtonFocused]}
              tvParallaxProperties={{ enabled: false }}
              focusable={false}
            >
              <Text style={[styles.closeButtonText, isCloseFocused && styles.closeButtonTextFocused]}>Close</Text>
            </Pressable>
          </View>
        </View>
      </View>
    </Modal>
  );
};

const createStyles = (theme: NovaTheme, screenWidth: number) => {
  // Responsive breakpoints
  const isNarrow = screenWidth < 400;
  const isMedium = screenWidth >= 400 && screenWidth < 600;

  // Responsive width: fill more on narrow screens
  const modalWidth = isNarrow ? '95%' : isMedium ? '92%' : '85%';
  const modalMaxWidth = isNarrow ? 420 : 800;

  // Responsive padding - minimize on narrow screens so cards fill width
  const horizontalPadding = isNarrow ? theme.spacing.sm : theme.spacing.xl;
  const resultMargin = isNarrow ? 0 : isMedium ? theme.spacing.xs : theme.spacing.sm;

  return StyleSheet.create({
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
      width: modalWidth,
      maxWidth: modalMaxWidth,
      maxHeight: '90%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: isNarrow ? theme.radius.lg : theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
    },
    modalHeader: {
      paddingHorizontal: horizontalPadding,
      paddingVertical: isNarrow ? theme.spacing.md : theme.spacing.lg,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      gap: theme.spacing.xs,
    },
    modalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      fontSize: isNarrow ? 18 : theme.typography.title.xl.fontSize,
    },
    modalSubtitle: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    languageSelector: {
      paddingHorizontal: horizontalPadding,
      paddingVertical: theme.spacing.md,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      flexDirection: 'row',
      alignItems: 'center',
      gap: isNarrow ? theme.spacing.sm : theme.spacing.md,
    },
    languageLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontWeight: '600',
      paddingRight: theme.spacing.md,
    },
    languageScrollView: {
      flex: 1,
      minHeight: 36,
    },
    languageList: {
      alignItems: 'center',
      paddingRight: theme.spacing.md,
    },
    languageChip: {
      paddingHorizontal: theme.spacing.md,
      height: 32,
      justifyContent: 'center',
      alignItems: 'center',
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
      padding: isNarrow ? theme.spacing.xs : isMedium ? theme.spacing.sm : theme.spacing.lg,
      gap: theme.spacing.sm,
    },
    resultItem: {
      padding: isNarrow ? theme.spacing.sm : theme.spacing.md,
      marginHorizontal: resultMargin,
      marginBottom: theme.spacing.sm,
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
      paddingHorizontal: horizontalPadding,
      paddingVertical: isNarrow ? theme.spacing.md : theme.spacing.lg,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'center',
    },
    closeButton: {
      minWidth: isNarrow ? 140 : 200,
      paddingHorizontal: isNarrow ? theme.spacing.xl : theme.spacing['2xl'],
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
};
