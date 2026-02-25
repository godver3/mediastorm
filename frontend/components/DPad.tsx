import React from 'react';
import { View, Pressable, StyleSheet } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { useTheme } from '@/theme';
import type { Direction } from '@/hooks/useKonamiCode';

interface DPadProps {
  onInput: (direction: Direction) => void;
}

export const DPad: React.FC<DPadProps> = ({ onInput }) => {
  const theme = useTheme();

  return (
    <View style={styles.container}>
      <View style={styles.dpadSection}>
        <View style={styles.row}>
          <Pressable
            onPress={() => onInput('up')}
            style={({ pressed }) => [
              styles.button,
              pressed && { backgroundColor: theme.colors.border.subtle },
            ]}
          >
            <Ionicons name="chevron-up" size={14} color={theme.colors.text.muted} />
          </Pressable>
        </View>
        <View style={styles.row}>
          <Pressable
            onPress={() => onInput('left')}
            style={({ pressed }) => [
              styles.button,
              pressed && { backgroundColor: theme.colors.border.subtle },
            ]}
          >
            <Ionicons name="chevron-back" size={14} color={theme.colors.text.muted} />
          </Pressable>
          <View style={styles.center} />
          <Pressable
            onPress={() => onInput('right')}
            style={({ pressed }) => [
              styles.button,
              pressed && { backgroundColor: theme.colors.border.subtle },
            ]}
          >
            <Ionicons name="chevron-forward" size={14} color={theme.colors.text.muted} />
          </Pressable>
        </View>
        <View style={styles.row}>
          <Pressable
            onPress={() => onInput('down')}
            style={({ pressed }) => [
              styles.button,
              pressed && { backgroundColor: theme.colors.border.subtle },
            ]}
          >
            <Ionicons name="chevron-down" size={14} color={theme.colors.text.muted} />
          </Pressable>
        </View>
      </View>

      <View style={styles.buttonSection}>
        <Pressable
          onPress={() => onInput('tap')}
          style={({ pressed }) => [
            styles.actionButton,
            pressed && { backgroundColor: theme.colors.border.subtle },
          ]}
        >
          <Ionicons name="ellipse-outline" size={14} color={theme.colors.text.muted} />
        </Pressable>
        <Pressable
          onPress={() => onInput('tap')}
          style={({ pressed }) => [
            styles.actionButton,
            pressed && { backgroundColor: theme.colors.border.subtle },
            { marginTop: -10, marginLeft: 15 },
          ]}
        >
          <Ionicons name="ellipse-outline" size={14} color={theme.colors.text.muted} />
        </Pressable>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 10,
    opacity: 0.4,
    gap: 20,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255,255,255,0.03)',
  },
  dpadSection: {
    alignItems: 'center',
  },
  buttonSection: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  row: {
    flexDirection: 'row',
  },
  button: {
    width: 28,
    height: 28,
    justifyContent: 'center',
    alignItems: 'center',
    borderRadius: 14,
  },
  actionButton: {
    width: 24,
    height: 24,
    justifyContent: 'center',
    alignItems: 'center',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: 'transparent',
  },
  center: {
    width: 28,
    height: 28,
  },
});
