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
    <View style={[{ flex: 1, backgroundColor: '#0b0b0f' }, style]} {...props}>
      {children}
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
