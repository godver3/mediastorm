import React, { useMemo } from 'react';
import { ActivityIndicator, Platform, StyleSheet, Text, View } from 'react-native';
import { tvScale } from '@/theme/tokens/tvScale';

export type AutoSubtitleStatus = 'idle' | 'searching' | 'downloading' | 'ready' | 'failed' | 'no-results';

interface SubtitleStatusOverlayProps {
  status: AutoSubtitleStatus;
  message: string | null;
}

const isTV = Platform.isTV;

export const SubtitleStatusOverlay: React.FC<SubtitleStatusOverlayProps> = ({ status, message }) => {
  const styles = useMemo(() => createStyles(), []);

  if (!message || status === 'idle') return null;

  return (
    <View style={styles.container}>
      <View style={styles.messageBox}>
        {(status === 'searching' || status === 'downloading') && (
          <ActivityIndicator size="small" color="#fff" style={styles.spinner} />
        )}
        <Text style={styles.text}>{message}</Text>
      </View>
    </View>
  );
};

const createStyles = () => {
  // TV values designed for tvOS, auto-scaled for Android TV
  const paddingH = tvScale(32, 16);
  const paddingV = tvScale(16, 8);
  const borderRadius = tvScale(8, 4);
  const fontSize = tvScale(28, 14);
  const spinnerMargin = tvScale(24, 12);

  return StyleSheet.create({
    container: {
      position: 'absolute',
      bottom: tvScale(100, 100),
      left: 0,
      right: 0,
      alignItems: 'center',
      pointerEvents: 'none',
    },
    messageBox: {
      flexDirection: 'row',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.7)',
      paddingHorizontal: paddingH,
      paddingVertical: paddingV,
      borderRadius: borderRadius,
    },
    spinner: {
      marginRight: spinnerMargin,
    },
    text: {
      color: '#fff',
      fontSize: fontSize,
      ...(isTV && {
        fontWeight: '600',
        textShadowColor: 'rgba(0, 0, 0, 0.8)',
        textShadowOffset: { width: 1, height: 1 },
        textShadowRadius: 4,
      }),
    },
  });
};
