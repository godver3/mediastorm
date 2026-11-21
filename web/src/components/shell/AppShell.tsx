import * as RadixDialog from '@radix-ui/react-dialog';
import { Outlet, useRouterState } from '@tanstack/react-router';
import { useEffect, useState } from 'react';
import { readJSON, STORAGE_KEYS, writeJSON } from '@/api/storage';
import { useAccent } from '@/auth/hooks';
import { cn } from '@/lib/cn';
import { IconButton } from '../IconButton';
import { ProfileMenu } from './ProfileMenu';
import { SearchBox } from './SearchBox';
import { Brand, Sidebar } from './Sidebar';

/**
 * Authenticated layout: collapsible sidebar on >= lg, drawer below; sticky
 * glass top bar with global search and the profile menu.
 */
export function AppShell() {
  useAccent();
  const [collapsed, setCollapsed] = useState(
    () => readJSON<boolean>(STORAGE_KEYS.sidebar) ?? false,
  );
  const [drawerOpen, setDrawerOpen] = useState(false);
  const pathname = useRouterState({ select: (s) => s.location.pathname });

  useEffect(() => writeJSON(STORAGE_KEYS.sidebar, collapsed), [collapsed]);
  useEffect(() => setDrawerOpen(false), [pathname]);

  return (
    <div className="flex min-h-full">
      {/* The column (and its glass background) stretches to the full page height;
          the nav inside sticks to the viewport while the page scrolls. */}
      <aside
        className={cn(
          'glass relative z-30 hidden shrink-0 self-stretch border-y-0 border-l-0 transition-[width] duration-200 lg:block',
          collapsed ? 'w-[72px]' : 'w-[232px]',
        )}
      >
        <div className="sticky top-0 h-dvh">
          <Sidebar collapsed={collapsed} />
        </div>
      </aside>

      <RadixDialog.Root open={drawerOpen} onOpenChange={setDrawerOpen}>
        <RadixDialog.Portal>
          <RadixDialog.Overlay className="fixed inset-0 z-40 bg-black/60 backdrop-blur-sm lg:hidden" />
          <RadixDialog.Content
            className="glass fixed inset-y-0 left-0 z-50 w-[260px] shadow-card lg:hidden"
            aria-describedby={undefined}
          >
            <RadixDialog.Title className="sr-only">Navigation</RadixDialog.Title>
            <Sidebar collapsed={false} onNavigate={() => setDrawerOpen(false)} />
          </RadixDialog.Content>
        </RadixDialog.Portal>
      </RadixDialog.Root>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="glass sticky top-0 z-20 flex h-16 items-center gap-3 border-x-0 border-t-0 px-3 sm:px-6">
          <IconButton
            icon="menu"
            label="Open navigation"
            className="lg:hidden"
            onClick={() => setDrawerOpen(true)}
          />
          <IconButton
            icon="sidebar"
            label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
            className="max-lg:hidden"
            onClick={() => setCollapsed((c) => !c)}
          />
          <div className="lg:hidden max-sm:hidden">
            <Brand />
          </div>
          <div className="flex flex-1 justify-center">
            <SearchBox />
          </div>
          <ProfileMenu />
        </header>
        <main className="min-w-0 flex-1">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
