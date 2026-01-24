import { Platform, StyleSheet } from 'react-native';
import type { NovaTheme } from '@/theme';
import { isTV, isAndroidTV, getTVScaleMultiplier } from '@/theme/tokens/tvScale';

export const createDetailsStyles = (theme: NovaTheme) => {
  // Unified TV scaling - tvOS is baseline (1.0), Android TV auto-derives for spacing/layout
  const tvScale = isTV ? getTVScaleMultiplier() : 1;
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
    genresRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: Math.round(8 * tvTextScale),
      marginBottom: theme.spacing.md,
      ...(isTV
        ? {
            marginLeft: tvScale * 48,
          }
        : null),
    },
    genreBadge: {
      backgroundColor: 'rgba(255, 255, 255, 0.12)',
      paddingHorizontal: Math.round(12 * tvTextScale),
      paddingVertical: Math.round(6 * tvTextScale),
      borderRadius: Math.round(16 * tvScale),
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: 'rgba(255, 255, 255, 0.2)',
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
      paddingVertical: theme.spacing.md * tvTextScale,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md * tvTextScale,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.accent.primary,
      justifyContent: 'center',
      alignItems: 'center',
      alignSelf: 'stretch',
    },
    progressIndicatorCompact: {
      paddingHorizontal: theme.spacing.sm * tvTextScale,
      paddingVertical: theme.spacing.sm * tvTextScale,
      minWidth: theme.spacing['2xl'] * 1.5,
      alignSelf: 'stretch',
    },
    progressIndicatorText: {
      ...theme.typography.label.md,
      color: theme.colors.accent.primary,
      fontWeight: '600',
      ...(isTV
        ? {
            // Design for tvOS at 1.375x, Android TV auto-scales
            fontSize: Math.round(theme.typography.label.md.fontSize * tvTextScale),
            lineHeight: Math.round(theme.typography.label.md.lineHeight * tvTextScale),
          }
        : null),
    },
    progressIndicatorTextCompact: {
      ...theme.typography.label.md,
      fontSize: Math.round(theme.typography.label.md.fontSize * tvTextScale),
      lineHeight: Math.round(theme.typography.label.md.lineHeight * tvTextScale),
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
  });
};
