import { useNavigate, useRouter } from '@tanstack/react-router';
import { useEffect, useRef, type MouseEvent, type ReactNode } from 'react';
import { PosterCard } from '@/components/MediaCard';
import { Skeleton, Spinner } from '@/components/Spinner';
import { cn } from '@/lib/cn';
import { Glyph } from './Glyph';

/** Responsive poster grid that fills the width (≈2 columns at 390px, 7–9 on desktop). */
export function MediaGrid({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={cn(
        'grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-x-4 gap-y-6',
        'sm:grid-cols-[repeat(auto-fill,minmax(160px,1fr))] lg:gap-x-5',
        className,
      )}
    >
      {children}
    </div>
  );
}

export function MediaGridSkeleton({ count = 18 }: { count?: number }) {
  return (
    <MediaGrid>
      {Array.from({ length: count }, (_, i) => (
        <div key={i}>
          <Skeleton className="aspect-[2/3] w-full rounded-card" />
          <Skeleton className="mt-2 h-3.5 w-3/4" />
          <Skeleton className="mt-1.5 h-3 w-1/3" />
        </div>
      ))}
    </MediaGrid>
  );
}

/** Loosely typed router destination ({ to, params, search }); callers pass typed literals. */
export interface LinkTarget {
  to: string;
  params?: Record<string, string>;
  search?: object;
}

export interface GridPosterProps {
  title: string;
  subtitle?: ReactNode;
  imageUrl?: string;
  badge?: ReactNode;
  /** Router destination (rendered as a real link, so middle-click / new tab work). */
  link?: LinkTarget;
  onClick?: () => void;
  /** Shows a hover / focus "remove" button in the corner. */
  onRemove?: () => void;
  removeLabel?: string;
  /** Watched tick in the corner. */
  watched?: boolean;
  busy?: boolean;
}

/** A PosterCard stretched to its grid cell, with an optional remove action beside it. */
export function GridPoster({
  title,
  subtitle,
  imageUrl,
  badge,
  link,
  onClick,
  onRemove,
  removeLabel = 'Remove',
  watched,
  busy,
}: GridPosterProps) {
  const router = useRouter();
  const navigate = useNavigate();
  const href = link ? router.buildLocation(link as never).href : undefined;
  const handleClick = (e: MouseEvent<HTMLElement>) => {
    if (link) {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
      e.preventDefault();
      void navigate(link as never);
    } else {
      onClick?.();
    }
  };
  return (
    <div className="group/poster relative min-w-0">
      <PosterCard
        title={title}
        subtitle={subtitle}
        imageUrl={imageUrl}
        badge={badge}
        href={href}
        onClick={handleClick}
        className="w-full! snap-none"
      />
      {watched && (
        <span
          className="pointer-events-none absolute top-2 right-2 flex h-6 w-6 items-center justify-center rounded-full bg-accent text-accent-contrast shadow"
          title="Watched"
        >
          <Glyph name="check" size={14} strokeWidth={2.6} />
          <span className="sr-only">Watched</span>
        </span>
      )}
      {onRemove && (
        <button
          type="button"
          onClick={onRemove}
          aria-label={`${removeLabel}: ${title}`}
          title={removeLabel}
          className={cn(
            'glass absolute top-2 right-2 flex h-8 w-8 cursor-pointer items-center justify-center rounded-full text-fg transition',
            'opacity-0 group-hover/poster:opacity-100 focus-visible:opacity-100 hover:text-danger',
            'outline-none focus-visible:ring-2 focus-visible:ring-accent max-sm:opacity-100 max-sm:h-7 max-sm:w-7',
          )}
        >
          <Glyph name="trash" size={15} />
        </button>
      )}
      {busy && (
        <div className="absolute inset-0 flex aspect-[2/3] items-center justify-center rounded-card bg-black/40 text-white">
          <Spinner />
        </div>
      )}
    </div>
  );
}

/** Calls `onVisible` when scrolled into view (infinite paging). */
export function LoadMoreSentinel({
  onVisible,
  loading,
  hasMore,
}: {
  onVisible: () => void;
  loading: boolean;
  hasMore: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const cb = useRef(onVisible);
  useEffect(() => {
    cb.current = onVisible;
  });
  useEffect(() => {
    const el = ref.current;
    if (!el || !hasMore || typeof IntersectionObserver === 'undefined') return;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) cb.current();
      },
      { rootMargin: '600px 0px' },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [hasMore]);
  if (!hasMore) return null;
  return (
    <div ref={ref} className="flex justify-center py-10 text-muted">
      {loading ? (
        <Spinner />
      ) : (
        <button
          type="button"
          onClick={onVisible}
          className="cursor-pointer rounded-full border border-line px-4 py-2 text-sm hover:text-fg"
        >
          Load more
        </button>
      )}
    </div>
  );
}
