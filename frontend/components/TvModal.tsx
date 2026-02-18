/**
 * TvModal - A modal component that works properly with tvOS/Android TV focus
 *
 * React Native's Modal component breaks focus navigation because it renders
 * in a separate root view. This component renders the modal content in the same
 * tree, allowing native TV focus to work correctly.
 */

import { useTheme } from '@/theme';
import { ReactNode, useEffect, useMemo, useRef } from 'react';
import { Animated, Pressable, StyleSheet, View } from 'react-native';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';

interface TvModalProps {
  visible: boolean;
  onRequestClose: () => void;
  children: ReactNode;
  transparent?: boolean;
  animationType?: 'none' | 'fade' | 'slide';
  withBackdrop?: boolean; // If false, no default scrim/backdrop will be rendered
}

export function TvModal({
  visible,
  onRequestClose,
  children,
  transparent = true,
  animationType = 'fade',
  withBackdrop = true,
}: TvModalProps) {
  const theme = useTheme();
  const fadeAnim = useMemo(() => new Animated.Value(0), []);
  const onRequestCloseRef = useRef(onRequestClose);
  const removeInterceptorRef = useRef<(() => void) | null>(null);

  // Keep the ref up to date
  useEffect(() => {
    onRequestCloseRef.current = onRequestClose;
  }, [onRequestClose]);

  // Handle back button via RemoteControlManager interceptor
  // RemoteControlManager already handles BackHandler events, so we just need to
  // push our interceptor which will run before GoBackConfiguration's interceptor
  useEffect(() => {
    if (!visible) {
      // Clean up interceptor when modal is hidden
      if (removeInterceptorRef.current) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
      return;
    }

    // Install interceptor when modal is shown
    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      onRequestCloseRef.current();
      return true; // Handled - prevents further interceptors from running
    });

    removeInterceptorRef.current = removeInterceptor;

    // Cleanup on unmount or when modal closes
    return () => {
      if (removeInterceptorRef.current) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
    };
  }, [visible]);

  // Animate in/out
  useEffect(() => {
    if (animationType === 'none') {
      fadeAnim.setValue(visible ? 1 : 0);
      return;
    }

    if (animationType === 'fade') {
      Animated.timing(fadeAnim, {
        toValue: visible ? 1 : 0,
        duration: 200,
        useNativeDriver: true,
      }).start();
    }
  }, [visible, animationType, fadeAnim]);

  if (!visible && animationType === 'none') return null;

  return (
    <Animated.View
      renderToHardwareTextureAndroid={true}
      style={[
        styles.container,
        {
          opacity: animationType === 'fade' ? fadeAnim : 1,
          pointerEvents: visible ? 'auto' : 'none',
        },
      ]}>
      {/* Backdrop (optional) */}
      {withBackdrop ? (
        <Pressable
          style={[
            styles.backdrop,
            {
              backgroundColor: transparent ? 'rgba(0, 0, 0, 0.85)' : theme.colors.background.base,
            },
          ]}
          onPress={onRequestClose}
          android_disableSound
          // Disable native tvOS parallax effects on backdrop
          tvParallaxProperties={{ enabled: false }}
        />
      ) : null}

      {/* Modal content - native focus handles navigation */}
      <View style={styles.contentContainer} pointerEvents="box-none">
        {children}
      </View>
    </Animated.View>
  );
}

const styles = StyleSheet.create({
  container: {
    ...StyleSheet.absoluteFillObject,
    zIndex: 1000,
  },
  backdrop: {
    ...StyleSheet.absoluteFillObject,
  },
  contentContainer: {
    ...StyleSheet.absoluteFillObject,
    justifyContent: 'center',
    alignItems: 'center',
  },
});

export default TvModal;
