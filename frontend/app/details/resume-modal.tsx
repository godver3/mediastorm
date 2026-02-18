import type { NovaTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { useCallback } from 'react';
import { Modal, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  SpatialNavigationFocusableView,
  DefaultFocus,
} from '@/services/tv-navigation';

interface ResumePlaybackModalProps {
  visible: boolean;
  onClose: () => void;
  onResume: () => void;
  onPlayFromBeginning: () => void;
  theme: NovaTheme;
  percentWatched: number;
}

export const ResumePlaybackModal = ({
  visible,
  onClose,
  onResume,
  onPlayFromBeginning,
  theme,
  percentWatched,
}: ResumePlaybackModalProps) => {
  const styles = createStyles(theme);

  const handleResume = useCallback(() => {
    onResume();
    onClose();
  }, [onResume, onClose]);

  const handlePlayFromBeginning = useCallback(() => {
    onPlayFromBeginning();
    onClose();
  }, [onPlayFromBeginning, onClose]);

  if (!visible) {
    return null;
  }
  const formattedPercent = Math.round(percentWatched);

  return (
    <Modal transparent visible={visible} onRequestClose={onClose} animationType="fade">
      <SpatialNavigationRoot isActive={visible}>
        <View style={styles.overlay}>
          <Pressable style={styles.overlayPressable} onPress={onClose} />
          <View style={styles.modalWrapper}>
            <View style={styles.modal}>
              <View style={styles.header}>
                <Text style={styles.title}>Resume Playback</Text>
                {!Platform.isTV && (
                  <Pressable onPress={onClose} style={styles.closeButton} hitSlop={8}>
                    <Ionicons name="close" size={24} color={theme.colors.text.primary} />
                  </Pressable>
                )}
              </View>

              <View style={styles.content}>
                <Text style={styles.description}>
                  You've watched {formattedPercent}% of this content. Would you like to resume where you left off or
                  start from the beginning?
                </Text>

                <SpatialNavigationNode orientation="vertical" focusKey="resume-modal-options">
                  <View style={styles.optionsContainer}>
                    <DefaultFocus>
                      <SpatialNavigationFocusableView focusKey="resume-button" onSelect={handleResume}>
                        {({ isFocused }: { isFocused: boolean }) => (
                          <Pressable
                            style={[styles.option, isFocused && styles.optionFocused]}
                            onPress={!Platform.isTV ? handleResume : undefined}
                            android_disableSound
                            tvParallaxProperties={{ enabled: false }}>
                            <View style={styles.optionContent}>
                              <Ionicons
                                name="play-circle"
                                size={32}
                                color={
                                  isFocused && Platform.isTV ? theme.colors.text.inverse : theme.colors.accent.primary
                                }
                              />
                              <View style={styles.optionText}>
                                <Text
                                  style={[styles.optionTitle, isFocused && Platform.isTV && styles.optionTitleFocused]}>
                                  Resume
                                </Text>
                                <Text
                                  style={[
                                    styles.optionDescription,
                                    isFocused && Platform.isTV && styles.optionDescriptionFocused,
                                  ]}>
                                  Continue from {formattedPercent}%
                                </Text>
                              </View>
                            </View>
                          </Pressable>
                        )}
                      </SpatialNavigationFocusableView>
                    </DefaultFocus>

                    <SpatialNavigationFocusableView
                      focusKey="play-from-beginning-button"
                      onSelect={handlePlayFromBeginning}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <Pressable
                          style={[styles.option, isFocused && styles.optionFocused]}
                          onPress={!Platform.isTV ? handlePlayFromBeginning : undefined}
                          android_disableSound
                          tvParallaxProperties={{ enabled: false }}>
                          <View style={styles.optionContent}>
                            <Ionicons
                              name="refresh-circle"
                              size={32}
                              color={
                                isFocused && Platform.isTV ? theme.colors.text.inverse : theme.colors.text.secondary
                              }
                            />
                            <View style={styles.optionText}>
                              <Text
                                style={[styles.optionTitle, isFocused && Platform.isTV && styles.optionTitleFocused]}>
                                Play from Beginning
                              </Text>
                              <Text
                                style={[
                                  styles.optionDescription,
                                  isFocused && Platform.isTV && styles.optionDescriptionFocused,
                                ]}>
                                Start over from 0%
                              </Text>
                            </View>
                          </View>
                        </Pressable>
                      )}
                    </SpatialNavigationFocusableView>
                  </View>
                </SpatialNavigationNode>
              </View>
            </View>
          </View>
        </View>
      </SpatialNavigationRoot>
    </Modal>
  );
};

const createStyles = (theme: NovaTheme) => {
  return StyleSheet.create({
    overlay: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
    },
    overlayPressable: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
    },
    modalWrapper: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      justifyContent: 'center',
      alignItems: 'center',
      padding: theme.spacing.xl,
      pointerEvents: 'box-none',
    },
    modal: {
      width: '100%',
      maxWidth: 500,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg,
      shadowColor: '#000',
      shadowOffset: {
        width: 0,
        height: 8,
      },
      shadowOpacity: 0.5,
      shadowRadius: 16,
      elevation: 24,
    },
    header: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      padding: theme.spacing.xl,
      borderBottomWidth: 1,
      borderBottomColor: theme.colors.border.subtle,
    },
    title: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    closeButton: {
      padding: theme.spacing.sm,
    },
    content: {
      padding: theme.spacing.xl,
      gap: theme.spacing.lg,
    },
    description: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      marginBottom: theme.spacing.md,
    },
    optionsContainer: {
      gap: theme.spacing.lg,
    },
    option: {
      padding: theme.spacing.lg,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.base,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
    },
    optionFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
      transform: Platform.isTV ? [{ scale: 1.05 }] : [],
    },
    optionContent: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.lg,
    },
    optionText: {
      flex: 1,
    },
    optionTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
    },
    optionTitleFocused: {
      color: theme.colors.text.inverse,
    },
    optionDescription: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    optionDescriptionFocused: {
      color: theme.colors.text.inverse,
    },
  });
};
