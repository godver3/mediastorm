import { Platform, StyleSheet } from 'react-native';
import type { NovaTheme } from '@/theme';
import { isTV, isAndroidTV, getTVScaleMultiplier, TV_REFERENCE_HEIGHT } from '@/theme/tokens/tvScale';

export const createDetailsStyles = (theme: NovaTheme, screenHeight = 0) => {
  // Unified TV scaling - tvOS is baseline (1.0), Android TV auto-derives for spacing/layout
  const tvScale = isTV ? getTVScaleMultiplier() : 1;
  // Viewport-height-based scale — consistent sizing across tvOS and Android TV
  const tvViewportScale = isTV && screenHeight > 0 ? screenHeight / TV_REFERENCE_HEIGHT : tvScale;
  // Text scale for UI elements with hardcoded pixel values (ratings, release info, etc.)
  const tvTextScale = isTV ? 1.2 * tvScale : 1;
  // Title/description scale - these use theme typography which is mobile-sized
  // tvOS: 1.2x, Android TV: 1.0x (no scaling needed)
  const tvTitleScale = isTV ? (isAndroidTV ? 1.0 : 1.2) : 1;

  return StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    container: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
      position: 'relative',
    },
    backgroundImageContainer: {
      ...StyleSheet.absoluteFillObject,
      alignItems: 'center',
      justifyContent: 'center',
      overflow: 'hidden',
    },
    backgroundImageContainerTop: {
      justifyContent: 'flex-start',
    },
    backgroundImage: {
      opacity: Platform.isTV ? 1 : 0.3,
      zIndex: 1,
    },
    backgroundImageSharp: {
      opacity: 1,
    },
    backgroundImageFill: {
      width: '100%',
      height: '100%',
    },
    // Absolute, full-bleed layer for blurred backdrop fill
    backgroundImageBackdrop: {
      ...StyleSheet.absoluteFillObject,
      zIndex: 0,
    },
    heroFadeOverlay: {
      position: 'absolute',
      left: 0,
      right: 0,
      bottom: 0,
      height: Platform.isTV ? '25%' : '65%',
      zIndex: 3,
    },
    gradientOverlay: {
      ...StyleSheet.absoluteFillObject,
      zIndex: 2,
    },
    contentOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'flex-end',
      zIndex: 4,
    },
    contentBox: {
      width: '100%',
      position: 'relative',
    },
    contentBoxInner: {
      flex: 1,
    },
    contentBoxConfined: {
      flex: 1,
      overflow: 'hidden',
    },
    contentMask: {
      ...StyleSheet.absoluteFillObject,
    },
    contentContainer: {
      flex: 1,
      paddingHorizontal: theme.spacing['3xl'],
      paddingVertical: theme.spacing['3xl'],
      gap: theme.spacing['2xl'],
      ...(Platform.isTV ? { flexDirection: 'column', justifyContent: 'flex-end' } : null),
    },
    mobileContentContainer: {
      justifyContent: 'flex-end',
    },
    touchContentScroll: {
      flex: 1,
    },
    touchContentContainer: {
      paddingHorizontal: theme.spacing['3xl'],
      paddingTop: theme.spacing['3xl'],
      paddingBottom: theme.spacing['3xl'],
      gap: theme.spacing['2xl'],
      minHeight: '100%',
      justifyContent: 'flex-end',
    },
    topContent: {},
    topContentTV: {
      // Content grows naturally - spacer height adjusts dynamically to keep action row at consistent position
    },
    topContentMobile: {
      backgroundColor: 'rgba(0, 0, 0, 0.35)',
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
      overflow: 'visible',
    },
    bottomContent: {
      ...(Platform.isTV ? { flex: 0, marginTop: tvScale * 16 } : null),
      position: 'relative',
    },
    mobileBottomContent: {
      flexDirection: 'column-reverse',
    },
    titleRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.md,
      marginBottom: theme.spacing.lg,
      overflow: 'visible',
      ...(isTV ? { maxWidth: '70%', marginLeft: tvScale * 48 } : null),
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      ...(isTV
        ? {
            // TV title - use tvTitleScale (no Android TV reduction for readability)
            fontSize: Math.round(theme.typography.title.xl.fontSize * tvTitleScale),
            lineHeight: Math.round(theme.typography.title.xl.lineHeight * tvTitleScale),
          }
        : null),
    },
    titleLogo: {
      // Bounding box approach - logo scales to fit within these constraints
      // while maintaining its natural aspect ratio
      maxWidth: isTV ? '30%' : '45%',
      maxHeight: isTV ? tvScale * 120 : 80,
      alignSelf: 'flex-start',
    },
    ratingsRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.md,
      marginBottom: theme.spacing.md,
      ...(isTV
        ? {
            marginLeft: tvScale * 48,
            // Reserve space for rating badges to prevent layout shift when data loads
            minHeight: Math.round(32 * tvScale),
          }
        : null),
    },
    ratingBadge: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: Math.round(4 * tvTextScale),
      backgroundColor: 'rgba(255, 255, 255, 0.1)',
      paddingHorizontal: Math.round(8 * tvTextScale),
      paddingVertical: Math.round(4 * tvTextScale),
      borderRadius: Math.round(6 * tvScale),
    },
    ratingValue: {
      fontSize: Math.round(14 * tvTextScale),
      fontWeight: '700',
    },
    ratingLabel: {
      fontSize: Math.round(12 * tvTextScale),
      color: theme.colors.text.secondary,
    },
    // Container for certification + genres side by side
    certificationGenresContainer: {
      flexDirection: 'row',
      alignItems: 'flex-start',
      gap: theme.spacing.lg,
      marginBottom: theme.spacing.md,
      ...(isTV ? { marginLeft: tvScale * 48 } : null),
    },
    // Large certification badge on the left
    certificationBadgeLarge: {
      justifyContent: 'center',
      alignItems: 'center',
    },
    // Genres column on the right
    genresColumn: {
      flex: 1,
      flexDirection: 'row',
      flexWrap: 'wrap',
      alignItems: 'center',
      gap: Math.round(8 * tvTextScale),
    },
    genresRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      alignItems: 'center',
      gap: Math.round(8 * tvTextScale),
      marginBottom: theme.spacing.md,
      ...(isTV
        ? {
            marginLeft: tvScale * 48,
          }
        : null),
    },
    genreBadge: {
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      paddingHorizontal: Math.round(12 * tvTextScale),
      paddingVertical: Math.round(6 * tvTextScale),
      borderRadius: Math.round(16 * tvScale),
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: 'rgba(255, 255, 255, 0.15)',
    },
    genreText: {
      fontSize: Math.round(12 * tvTextScale),
      fontWeight: '500',
      color: theme.colors.text.secondary,
    },
    releaseInfoRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      marginBottom: theme.spacing.md,
      ...(isTV
        ? {
            marginLeft: tvScale * 48,
            // Reserve space for release info to prevent layout shift when data loads
            minHeight: Math.round(40 * tvScale),
          }
        : null),
    },
    releaseInfoItem: {
      flexDirection: 'row',
      alignItems: 'center',
      marginRight: theme.spacing.lg,
      marginBottom: theme.spacing.sm,
    },
    releaseInfoIcon: {
      marginRight: 6,
    },
    releaseInfoValue: {
      color: theme.colors.text.secondary,
      // Design for tvOS, Android TV auto-scales
      fontSize: Math.round(14 * tvTextScale),
    },
    releaseInfoLoading: {
      color: theme.colors.text.secondary,
      fontSize: Math.round(14 * tvTextScale),
    },
    releaseInfoError: {
      color: theme.colors.status.danger,
      fontSize: Math.round(14 * tvTextScale),
    },
    watchlistEyeIcon: {
      marginTop: theme.spacing.xs,
    },
    description: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
      marginBottom: theme.spacing.sm,
      width: '100%',
      maxWidth: theme.breakpoint === 'compact' ? '100%' : '60%',
      ...(isTV
        ? {
            // TV description - use tvTitleScale (no Android TV reduction for readability)
            fontSize: Math.round(theme.typography.body.lg.fontSize * tvTitleScale),
            lineHeight: Math.round(theme.typography.body.lg.lineHeight * tvTitleScale),
            marginLeft: tvScale * 48,
          }
        : null),
    },
    descriptionToggle: {
      color: theme.colors.text.muted,
      fontSize: 14,
      marginTop: 4,
    },
    descriptionHidden: {
      position: 'absolute',
      opacity: 0,
      zIndex: -1,
    },
    readMoreButton: {
      alignSelf: 'flex-start',
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md,
      marginBottom: theme.spacing.lg,
      backgroundColor: theme.colors.overlay.button,
      borderRadius: theme.radius.md,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    actionRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.lg,
      ...(isTV ? { marginLeft: tvScale * 48, marginBottom: tvScale * 24 } : null),
    },
    compactActionRow: {
      flexWrap: 'nowrap',
      gap: theme.spacing.sm,
      maxWidth: '100%',
    },
    primaryActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
    },
    manualActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    debugActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.status.warning,
    },
    trailerActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    watchlistActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    watchlistActionButtonActive: {
      // No special background when active - let focus state handle styling
    },
    watchStateButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    watchStateButtonActive: {
      // No special background when active - let focus state handle styling
    },
    iconActionButton: {
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.sm,
      minWidth: theme.spacing['2xl'] * 1.5,
    },
    watchlistError: {
      marginTop: theme.spacing.md,
      color: theme.colors.status.danger,
      ...theme.typography.body.sm,
    },
    trailerError: {
      marginTop: theme.spacing.sm,
      color: theme.colors.status.danger,
      ...theme.typography.body.sm,
    },
    // Prequeue stream info display
    prequeueInfoContainer: {
      marginTop: theme.spacing.md,
      marginLeft: isTV ? tvScale * 48 : 0,
      // TV: no marginBottom here - TV track selection handles spacing, or next sections handle their own top margin
      marginBottom: isTV ? 0 : 0,
    },
    prequeueInfoMinHeight: {
      // Reserve enough height for filename to prevent layout shift (TV track buttons are outside this container)
      minHeight: isTV ? tvScale * 45 : 45,
    },
    prequeueFilename: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
      ...(isTV ? { fontSize: tvScale * 18 } : null),
    },
    prequeueTrackRow: {
      flexDirection: 'row',
      alignItems: 'center',
      marginTop: isTV ? theme.spacing.sm : theme.spacing.xs,
      gap: isTV ? theme.spacing.sm : theme.spacing.xs,
    },
    prequeueTrackValue: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.primary,
      flexShrink: 1,
      ...(isTV ? { fontSize: tvScale * 15 } : null),
    },
    prequeueTrackSeparator: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      marginHorizontal: isTV ? theme.spacing.sm : theme.spacing.xs,
      ...(isTV ? { fontSize: tvScale * 15 } : null),
    },
    prequeueTrackBadge: {
      ...theme.typography.caption.sm,
      fontSize: isTV ? tvScale * 15 : 10,
      fontWeight: '700',
      color: '#fff',
      paddingHorizontal: isTV ? theme.spacing.sm : theme.spacing.xs,
      paddingVertical: isTV ? 4 : 2,
      borderRadius: theme.radius.sm,
      overflow: 'hidden',
    },
    prequeueTrackCodecBadge: {
      backgroundColor: '#444',
    },
    prequeueTrackForcedBadge: {
      backgroundColor: '#e67e22',
    },
    prequeueTrackSDHBadge: {
      backgroundColor: '#27ae60',
    },
    prequeueLoadingText: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      fontStyle: 'italic',
      ...(isTV ? { fontSize: tvScale * 15 } : null),
    },
    prequeueTrackPressable: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: isTV ? theme.spacing.sm : theme.spacing.xs,
      // On mobile, limit width so both audio and subtitle fit on one line
      ...(isTV ? {
        paddingVertical: theme.spacing.xs,
        paddingHorizontal: theme.spacing.sm,
        borderRadius: theme.radius.sm,
      } : { flex: 1, maxWidth: '48%' }),
    },
    prequeueTrackFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    prequeueTrackValueFocused: {
      color: theme.colors.text.inverse,
    },
    tvTrackSelectionContainer: {
      // Match prequeueInfoContainer positioning for TV
      flexDirection: 'row' as const,
      alignItems: 'center' as const,
      gap: theme.spacing.sm,
      marginLeft: tvScale * 48,
      marginTop: -(tvScale * 16),
      marginBottom: tvScale * 16, // Space before next section
    },
    tvTrackSelectionPlaceholder: {
      // Same total height as tvTrackSelectionContainer + button row to prevent layout shift
      marginTop: -(tvScale * 16),
      marginBottom: tvScale * 16,
      height: tvScale * 32, // Matches track button row height (paddingVertical + text + icon)
    },
    episodeNavigationRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.md,
      marginBottom: theme.spacing.md,
    },
    episodeNavButton: {
      // Scale padding for TV - paddingVertical inherited from FocusablePressable for consistent height
      paddingHorizontal: Math.round(theme.spacing['2xl'] * tvTextScale),
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    mobileEpisodeNavRow: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'center',
      alignSelf: 'center',
      gap: theme.spacing.xs,
      marginBottom: theme.spacing.sm,
      backgroundColor: 'rgba(255, 255, 255, 0.15)',
      borderRadius: 24,
      paddingVertical: theme.spacing.xs,
      paddingHorizontal: theme.spacing.xs,
    },
    mobileEpisodeNavButton: {
      width: 36,
      height: 36,
      borderRadius: 18,
      backgroundColor: 'rgba(0, 0, 0, 0.4)',
      justifyContent: 'center',
      alignItems: 'center',
      paddingHorizontal: 0,
      paddingVertical: 0,
      minWidth: 36,
    },
    mobileEpisodeNavLabel: {
      color: '#fff',
      fontSize: 14,
      fontWeight: '600',
      paddingHorizontal: theme.spacing.xs,
    },
    episodeCardContainer: {
      marginBottom: theme.spacing.xl,
    },
    episodeCardWrapperTV: {
      width: '75%',
    },
    posterContainerTV: {
      position: 'absolute',
      right: theme.spacing.xl,
      bottom: theme.spacing.xl,
      width: '20%',
      aspectRatio: 2 / 3,
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
      zIndex: 5,
    },
    posterImageTV: {
      width: '100%',
      height: '100%',
    },
    posterGradientTV: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: '20%',
    },
    progressIndicator: {
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md * tvViewportScale,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md * tvViewportScale,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.accent.primary,
      justifyContent: 'center',
      alignItems: 'center',
      alignSelf: 'stretch',
    },
    progressIndicatorCompact: {
      paddingHorizontal: theme.spacing.sm * tvViewportScale,
      paddingVertical: theme.spacing.sm * tvViewportScale,
      minWidth: theme.spacing['2xl'] * 1.5,
      alignSelf: 'stretch',
    },
    progressIndicatorText: {
      ...theme.typography.label.md,
      color: theme.colors.accent.primary,
      fontWeight: '600',
    },
    progressIndicatorTextCompact: {
      ...theme.typography.label.md,
    },
    // Mobile episode overview styles
    episodeOverviewTitle: {
      ...theme.typography.body.md,
      fontWeight: '600',
      marginBottom: theme.spacing.xs,
    },
    episodeOverviewMeta: {
      ...theme.typography.caption.sm,
      marginTop: theme.spacing.sm,
      opacity: 0.7,
    },
    episodeOverviewText: {
      ...theme.typography.body.md,
      lineHeight: theme.typography.body.md.fontSize * 1.5,
    },

    // TV Scrollable Layout Styles
    tvScrollContainer: {
      ...StyleSheet.absoluteFillObject,
      zIndex: 4,
    },
    tvScrollContent: {
      flexGrow: 1,
    },
    tvContentGradient: {
      minHeight: '100%',
      paddingTop: tvScale * 60,
    },
    tvContentInner: {
      paddingBottom: tvScale * 32,
    },

    // More Options Menu Modal — matches BulkWatchModal visual style
    moreOptionsModal: {
      width: Platform.isTV ? '50%' : theme.breakpoint === 'compact' ? '90%' : '80%',
      maxWidth: Platform.isTV ? Math.round(700 * tvViewportScale) : 440,
      backgroundColor: theme.colors.background.surface,
      borderRadius: Math.round(theme.radius.lg * tvViewportScale),
      overflow: 'hidden' as const,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      ...Platform.select({
        ios: {
          shadowColor: '#000',
          shadowOffset: { width: 0, height: 4 },
          shadowOpacity: 0.3,
          shadowRadius: 8,
        },
        android: {
          elevation: 8,
        },
      }),
    },
    moreOptionsHeader: {
      paddingHorizontal: Math.round((Platform.isTV ? theme.spacing['3xl'] : theme.spacing['2xl']) * tvViewportScale),
      paddingVertical: Math.round((Platform.isTV ? theme.spacing['2xl'] : theme.spacing.xl) * tvViewportScale),
      borderBottomWidth: 1,
      borderBottomColor: theme.colors.border.subtle,
    },
    moreOptionsTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    moreOptionsContent: {
      paddingHorizontal: Math.round((Platform.isTV ? theme.spacing['3xl'] : theme.spacing['2xl']) * tvViewportScale),
      paddingVertical: Math.round((Platform.isTV ? theme.spacing['2xl'] : theme.spacing.xl) * tvViewportScale),
    },
    moreOptionsItem: {
      width: '100%' as const,
      flexDirection: 'row' as const,
      alignItems: 'center' as const,
      gap: Math.round((Platform.isTV ? theme.spacing.lg : theme.spacing.md) * tvViewportScale),
      backgroundColor: 'rgba(255, 255, 255, 0.08)' as const,
      borderRadius: Math.round(theme.radius.md * tvViewportScale),
      paddingVertical: Math.round((Platform.isTV ? theme.spacing.xl : theme.spacing.md) * tvViewportScale),
      paddingHorizontal: Math.round((Platform.isTV ? theme.spacing['2xl'] : theme.spacing.lg) * tvViewportScale),
      marginBottom: Math.round((Platform.isTV ? theme.spacing.lg : theme.spacing.md) * tvViewportScale),
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    moreOptionsItemFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    moreOptionsFooter: {
      paddingHorizontal: Math.round((Platform.isTV ? theme.spacing['3xl'] : theme.spacing['2xl']) * tvViewportScale),
      paddingVertical: Math.round((Platform.isTV ? theme.spacing['2xl'] : theme.spacing.xl) * tvViewportScale),
      borderTopWidth: 1,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'flex-end' as const,
    },
    moreOptionsCancelButton: {
      paddingHorizontal: Math.round(theme.spacing['2xl'] * tvViewportScale),
      paddingVertical: Math.round(theme.spacing.md * tvViewportScale),
      borderRadius: Math.round(theme.radius.md * tvViewportScale),
      alignItems: 'center' as const,
    },
    moreOptionsCancelFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    moreOptionsCancelButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
  });
};
