import React from 'react';
import { ActivityIndicator, Platform, StyleSheet, Text, View } from 'react-native';

export type AutoSubtitleStatus = 'idle' | 'searching' | 'downloading' | 'ready' | 'failed' | 'no-results';

interface SubtitleStatusOverlayProps {
  status: AutoSubtitleStatus;
  message: string | null;
}

export const SubtitleStatusOverlay: React.FC<SubtitleStatusOverlayProps> = ({
  status,
  message,
}) => {
  if (!message || status === 'idle') return null;

  const isTvPlatform = Platform.isTV;

  return (
    <View style={styles.container}>
      <View style={[styles.messageBox, isTvPlatform && styles.messageBoxTV]}>
        {(status === 'searching' || status === 'downloading') && (
          <ActivityIndicator
            size="small"
            color="#fff"
            style={[styles.spinner, isTvPlatform && styles.spinnerTV]}
          />
        )}
        <Text style={[styles.text, isTvPlatform && styles.textTV]}>{message}</Text>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    position: 'absolute',
    bottom: 100,
    left: 0,
    right: 0,
    alignItems: 'center',
    pointerEvents: 'none',
  },
  messageBox: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(0, 0, 0, 0.7)',
    paddingHorizontal: 16,
    paddingVertical: 8,
    borderRadius: 4,
  },
  messageBoxTV: {
    paddingHorizontal: 32,
    paddingVertical: 16,
    borderRadius: 8,
  },
  spinner: {
    marginRight: 12,
  },
  spinnerTV: {
    marginRight: 24,
  },
  text: {
    color: '#fff',
    fontSize: 14,
  },
  textTV: {
    fontSize: 28,
    fontWeight: '600',
    textShadowColor: 'rgba(0, 0, 0, 0.8)',
    textShadowOffset: { width: 1, height: 1 },
    textShadowRadius: 4,
  },
});
