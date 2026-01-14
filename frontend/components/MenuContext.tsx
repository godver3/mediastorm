import React, { createContext, useCallback, useContext, useMemo, useState } from 'react';

interface MenuContextType {
  isOpen: boolean;
  toggleMenu: (isOpen: boolean) => void;
  openMenu: () => void;
  closeMenu: () => void;
  /** Native tag of the first focusable item in content area (for TV focus navigation) */
  firstContentFocusableTag: number | null;
  setFirstContentFocusableTag: (tag: number | null) => void;
}

const MenuContext = createContext<MenuContextType | undefined>(undefined);

export const MenuProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [isOpen, setIsOpen] = useState(false);
  const [firstContentFocusableTag, setFirstContentFocusableTag] = useState<number | null>(null);

  const toggleMenu = useCallback((newIsOpen: boolean) => {
    setIsOpen(newIsOpen);
  }, []);

  const openMenu = useCallback(() => {
    setIsOpen(true);
  }, []);

  const closeMenu = useCallback(() => {
    setIsOpen(false);
  }, []);

  const value = useMemo(
    () => ({
      isOpen,
      toggleMenu,
      openMenu,
      closeMenu,
      firstContentFocusableTag,
      setFirstContentFocusableTag,
    }),
    [isOpen, toggleMenu, openMenu, closeMenu, firstContentFocusableTag],
  );

  return <MenuContext.Provider value={value}>{children}</MenuContext.Provider>;
};

// Default no-op context for when provider is not available (e.g., during route evaluation)
const defaultContext: MenuContextType = {
  isOpen: false,
  toggleMenu: () => {},
  openMenu: () => {},
  closeMenu: () => {},
  firstContentFocusableTag: null,
  setFirstContentFocusableTag: () => {},
};

export const useMenuContext = (): MenuContextType => {
  const context = useContext(MenuContext);
  // Return default context instead of throwing - expo-router may evaluate routes
  // before the provider tree is fully established
  if (context === undefined) {
    return defaultContext;
  }
  return context;
};
