import React from 'react';
import { Platform, StyleSheet, View } from 'react-native';

import { TVSidebar } from '@/components/TVSidebar';
import { TVSidebarProvider } from '@/components/TVSidebarContext';

interface TVPageLayoutProps {
  children: React.ReactNode;
}

/**
 * Layout wrapper for TV pages that includes the persistent sidebar.
 * On non-TV platforms, this just renders the children directly.
 *
 * Note: TVBackground is provided by the drawer layout, so we don't duplicate it here.
 *
 * Usage:
 * ```tsx
 * export default function MyPage() {
 *   return (
 *     <TVPageLayout>
 *       <MyPageContent />
 *     </TVPageLayout>
 *   );
 * }
 * ```
 */
export function TVPageLayout({ children }: TVPageLayoutProps) {
  // On non-TV platforms, just render children (they use bottom tabs)
  if (!Platform.isTV) {
    return <>{children}</>;
  }

  return (
    <TVSidebarProvider>
      <View style={styles.container}>
        <TVSidebar />
        <View style={styles.content}>
          {children}
        </View>
      </View>
    </TVSidebarProvider>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    flexDirection: 'row',
  },
  content: {
    flex: 1,
  },
});
