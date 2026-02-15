import { useCallback, useState } from 'react';
import { findNodeHandle } from 'react-native';
import type { View } from 'react-native';

export interface TVFocusTagResult {
  /** Attach to target View as ref */
  ref: React.RefCallback<View>;
  /** Resolved native tag (null until mounted) */
  tag: number | null;
}

/**
 * Resolves a View's native tag for use with TV focus APIs
 * (e.g. TVFocusGuideView destinations, nextFocusLeft/Right).
 *
 * Encapsulates the recurring pattern of useRef + findNodeHandle
 * found in ProfileSelectorModal, CategoryFilterModal, etc.
 */
export function useTVFocusTag(): TVFocusTagResult {
  const [tag, setTag] = useState<number | null>(null);

  const ref = useCallback((instance: View | null) => {
    if (instance) {
      const nativeTag = findNodeHandle(instance);
      setTag(nativeTag);
    } else {
      setTag(null);
    }
  }, []);

  return { ref, tag };
}
