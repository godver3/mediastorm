import { useTheme } from '@/theme';
import { useMemo } from 'react';
import { Platform } from 'react-native';

export const useShouldUseTabs = () => {
  const theme = useTheme();

  return useMemo(() => {
    // On mobile devices (non-TV iOS/Android), always use tabs regardless of screen width
    // This ensures phones, tablets, and foldables all get the mobile tab navigation
    const isMobileDevice = (Platform.OS === 'ios' || Platform.OS === 'android') && !Platform.isTV;
    if (isMobileDevice) {
      return true;
    }

    // For web and other platforms, use tabs only for compact breakpoint
    const isCompact = theme.breakpoint === 'compact';
    const isWebMobile = Platform.OS === 'web' && !Platform.isTV;

    return isCompact && isWebMobile;
  }, [theme.breakpoint]);
};

export default useShouldUseTabs;
