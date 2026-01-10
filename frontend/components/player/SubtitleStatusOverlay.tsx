import React from 'react';
import { ActivityIndicator, StyleSheet, Text, View } from 'react-native';

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
  spinner: {
    marginRight: 12,
  },
  text: {
    color: '#fff',
    fontSize: 14,
  },
});
