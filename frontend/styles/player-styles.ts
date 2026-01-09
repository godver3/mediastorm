import { Platform, StyleSheet } from 'react-native';
import type { NovaTheme } from '@/theme';

export const createPlayerStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: '#000000',
    },
    container: {
      flex: 1,
      backgroundColor: '#000000',
    },
    videoWrapper: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'stretch',
      backgroundColor: '#000000',
    },
    controlsContainer: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'space-between',
      zIndex: 1,
    },
    overlayAnimatedWrapper: {
      flex: 1,
    },
    overlayContent: {
      flex: 1,
      justifyContent: 'space-between',
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.lg,
    },
    overlayTopRow: {
      flexDirection: 'row',
      alignItems: 'flex-start',
      justifyContent: 'space-between',
    },
    overlayControls: {
      flex: 1,
      justifyContent: 'flex-end',
      position: 'relative',
      marginTop: theme.spacing.md,
    },
    topGradient: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      height: '20%',
      zIndex: 0,
    },
    bottomGradient: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: '20%',
      zIndex: 0,
    },
    pauseTeardownOverlay: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: '#000000',
      alignItems: 'center',
      justifyContent: 'center',
      zIndex: 0, // Below controls (zIndex: 1) so controls are visible and interactive
    },
    pauseTeardownImage: {
      width: '100%',
      height: '100%',
    },
    pauseTeardownPlaceholder: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: '#000000',
    },
    loadingOverlay: {
      ...StyleSheet.absoluteFillObject,
      alignItems: 'center',
      justifyContent: 'center',
      zIndex: 2,
    },
    debugOverlay: {
      ...StyleSheet.absoluteFillObject,
      alignItems: 'stretch',
      justifyContent: 'flex-end',
      zIndex: 3,
    },
    debugCard: {
      alignSelf: 'stretch',
      maxHeight: '60%',
      margin: 12,
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      borderRadius: 8,
    },
    debugScroll: {
      maxHeight: '100%',
      paddingHorizontal: 12,
      paddingVertical: 8,
    },
    debugScrollContent: {
      paddingBottom: 8,
    },
    debugLine: {
      fontSize: 12,
      lineHeight: 16,
      marginBottom: 6,
      fontFamily: Platform.select({ web: 'monospace', default: undefined }) || undefined,
      color: theme.colors.text.primary,
    },
    debugInfo: {
      color: '#9be7ff',
    },
    debugWarn: {
      color: '#ffe57f',
    },
    debugError: {
      color: '#ff8a80',
    },
  });
