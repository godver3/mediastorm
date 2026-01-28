import React, { createContext, useCallback, useContext, useMemo, useRef, useState } from 'react';
import { findNodeHandle, View } from 'react-native';

interface TVSidebarContextType {
  // Native tag of the first sidebar item (for content to set nextFocusLeft)
  sidebarFirstItemTag: number | null;
  setSidebarFirstItemRef: (ref: View | null) => void;

  // Native tag of the first content item (for sidebar to set nextFocusRight)
  contentFirstItemTag: number | null;
  setContentFirstItemRef: (ref: View | null) => void;

  // Track which sidebar item should receive focus when returning from content
  activeSidebarIndex: number;
  setActiveSidebarIndex: (index: number) => void;
}

const TVSidebarContext = createContext<TVSidebarContextType | undefined>(undefined);

export const TVSidebarProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [sidebarFirstItemTag, setSidebarFirstItemTag] = useState<number | null>(null);
  const [contentFirstItemTag, setContentFirstItemTag] = useState<number | null>(null);
  const [activeSidebarIndex, setActiveSidebarIndex] = useState(0);

  const setSidebarFirstItemRef = useCallback((ref: View | null) => {
    if (ref) {
      const tag = findNodeHandle(ref);
      setSidebarFirstItemTag(tag);
    } else {
      setSidebarFirstItemTag(null);
    }
  }, []);

  const setContentFirstItemRef = useCallback((ref: View | null) => {
    if (ref) {
      const tag = findNodeHandle(ref);
      setContentFirstItemTag(tag);
    } else {
      setContentFirstItemTag(null);
    }
  }, []);

  const value = useMemo(
    () => ({
      sidebarFirstItemTag,
      setSidebarFirstItemRef,
      contentFirstItemTag,
      setContentFirstItemRef,
      activeSidebarIndex,
      setActiveSidebarIndex,
    }),
    [sidebarFirstItemTag, setSidebarFirstItemRef, contentFirstItemTag, setContentFirstItemRef, activeSidebarIndex],
  );

  return <TVSidebarContext.Provider value={value}>{children}</TVSidebarContext.Provider>;
};

export const useTVSidebar = (): TVSidebarContextType => {
  const context = useContext(TVSidebarContext);
  if (context === undefined) {
    throw new Error('useTVSidebar must be used within a TVSidebarProvider');
  }
  return context;
};

// Optional hook that returns undefined if not in provider (for conditional use)
export const useTVSidebarOptional = (): TVSidebarContextType | undefined => {
  return useContext(TVSidebarContext);
};
