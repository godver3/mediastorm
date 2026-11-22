import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { IconButton } from './IconButton';

export interface ShelfProps {
  title: ReactNode;
  /** Optional right-side header content, e.g. a "See all" link. */
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}

/**
 * Horizontally scrolling row of cards. Arrow buttons appear on hover (desktop)
 * and page by ~90% of the visible width. Children should be fixed-width cards
 * (PosterCard / LandscapeCard set their own widths).
 */
export function Shelf({ title, action, children, className }: ShelfProps) {
  const scroller = useRef<HTMLDivElement>(null);
  const [canLeft, setCanLeft] = useState(false);
  const [canRight, setCanRight] = useState(false);

  const update = useCallback(() => {
    const el = scroller.current;
    if (!el) return;
    setCanLeft(el.scrollLeft > 4);
    setCanRight(el.scrollLeft + el.clientWidth < el.scrollWidth - 4);
  }, []);

  useEffect(() => {
    update();
    const el = scroller.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  }, [update, children]);

  const page = (dir: 1 | -1) => {
    const el = scroller.current;
    if (el) el.scrollBy({ left: dir * el.clientWidth * 0.9, behavior: 'smooth' });
  };

  return (
    <section className={cn('group/shelf relative py-3', className)}>
      <div className="mb-3 flex items-baseline justify-between gap-4 px-4 sm:px-6 lg:px-10">
        <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
        {action}
      </div>
      <div className="relative">
        <div
          ref={scroller}
          onScroll={update}
          className="no-scrollbar flex snap-x gap-4 overflow-x-auto scroll-px-4 px-4 py-2 sm:scroll-px-6 sm:px-6 lg:scroll-px-10 lg:px-10"
        >
          {children}
        </div>
        {canLeft && (
          <IconButton
            icon="chevronLeft"
            label="Scroll left"
            variant="glass"
            onClick={() => page(-1)}
            className="absolute top-1/2 left-2 -translate-y-1/2 opacity-0 transition group-hover/shelf:opacity-100 focus-visible:opacity-100 max-sm:hidden"
          />
        )}
        {canRight && (
          <IconButton
            icon="chevronRight"
            label="Scroll right"
            variant="glass"
            onClick={() => page(1)}
            className="absolute top-1/2 right-2 -translate-y-1/2 opacity-0 transition group-hover/shelf:opacity-100 focus-visible:opacity-100 max-sm:hidden"
          />
        )}
      </div>
    </section>
  );
}
