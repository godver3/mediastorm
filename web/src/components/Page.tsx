import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface PageProps {
  title?: ReactNode;
  subtitle?: ReactNode;
  /** Right-aligned header content (buttons, filters). */
  actions?: ReactNode;
  /** Full-bleed pages (hero banners) skip the horizontal padding. */
  bleed?: boolean;
  className?: string;
  children: ReactNode;
}

/** Standard page wrapper: consistent gutters, max width and header. */
export function Page({ title, subtitle, actions, bleed, className, children }: PageProps) {
  return (
    <div
      className={cn(
        'mx-auto w-full max-w-[1800px] pb-16',
        !bleed && 'px-4 sm:px-6 lg:px-10',
        className,
      )}
    >
      {(title || actions) && (
        <header
          className={cn(
            'flex flex-wrap items-end justify-between gap-4 pt-6 pb-6',
            bleed && 'px-4 sm:px-6 lg:px-10',
          )}
        >
          <div className="min-w-0">
            {title && (
              <h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">{title}</h1>
            )}
            {subtitle && <p className="mt-1 text-sm text-muted">{subtitle}</p>}
          </div>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </header>
      )}
      {children}
    </div>
  );
}
