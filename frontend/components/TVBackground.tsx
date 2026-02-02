import { useTheme } from '@/theme';
import { LinearGradient } from 'expo-linear-gradient';
import { Platform, StyleSheet, View, ViewProps } from 'react-native';

export function TVBackground({ children, style, ...props }: ViewProps) {
  const theme = useTheme();

  if (!Platform.isTV) {
    return (
      <View style={[{ flex: 1, backgroundColor: theme.colors.background.base }, style]} {...props}>
        {children}
      </View>
    );
  }

  return (
    <View style={[{ flex: 1, backgroundColor: theme.colors.background.base }, style]} {...props}>
      <View style={styles.gradientContainer}>
        <View style={styles.gradientLayer}>
          <LinearGradient
            colors={['#0c0517', '#120421', theme.colors.background.base]}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 0.85 }}
            style={StyleSheet.absoluteFill}
          />
        </View>
        <View style={[styles.gradientLayer, { opacity: 0.35 }]}>
          <LinearGradient
            colors={['rgba(232, 238, 255, 0.55)', 'rgba(40, 44, 54, 0.08)', 'rgba(210, 222, 255, 0.32)']}
            start={{ x: 0, y: 0 }}
            end={{ x: 0.95, y: 1 }}
            style={StyleSheet.absoluteFill}
          />
        </View>
        {/* Darkening gradient for bottom third */}
        <View style={styles.bottomDarkenLayer}>
          <LinearGradient
            colors={['transparent', 'rgba(0, 0, 0, 0.4)', 'rgba(0, 0, 0, 0.7)']}
            locations={[0, 0.5, 1]}
            start={{ x: 0, y: 0 }}
            end={{ x: 0, y: 1 }}
            style={StyleSheet.absoluteFill}
          />
        </View>
      </View>
      <View style={{ flex: 1, zIndex: 1 }}>{children}</View>
    </View>
  );
}

const styles = StyleSheet.create({
  gradientContainer: {
    ...StyleSheet.absoluteFillObject,
    overflow: 'hidden',
  },
  gradientLayer: {
    ...StyleSheet.absoluteFillObject,
    // Extend bounds slightly to ensure coverage if needed, though absoluteFill is usually enough
    top: -80,
    bottom: -80,
    left: -80,
    right: -80,
  },
  bottomDarkenLayer: {
    position: 'absolute',
    left: 0,
    right: 0,
    bottom: 0,
    height: '33.33%',
  },
});
