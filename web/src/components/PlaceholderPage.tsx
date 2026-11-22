import type { ReactNode } from 'react';
import { EmptyState } from './EmptyState';
import type { IconName } from './icons';
import { Page } from './Page';

/** Temporary page body for routes not built yet. Replace the route file's component. */
export function PlaceholderPage({
  title,
  icon,
  children,
}: {
  title: string;
  icon: IconName;
  children?: ReactNode;
}) {
  return (
    <Page title={title}>
      <div className="glass rounded-2xl">
        <EmptyState
          icon={icon}
          title={`${title} is coming soon`}
          description="This page has not been built in the new web client yet."
        />
        {children}
      </div>
    </Page>
  );
}
