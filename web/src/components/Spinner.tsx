import { cn } from '@/lib/cn';

export function Spinner({ size = 24, className }: { size?: number; className?: string }) {
  return (
    <span
      role="status"
      aria-label="Loading"
      className={cn(
        'inline-block animate-spin rounded-full border-2 border-current border-r-transparent',
        className,
      )}
      style={{ width: size, height: size }}
    />
  );
}

/** Shimmering placeholder block. Size it with className (e.g. "h-4 w-32"). */
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      aria-hidden="true"
      className={cn('animate-pulse rounded-lg bg-surface-strong', className)}
    />
  );
}

/** Centered full-area spinner for route-level loading. */
export function PageSpinner() {
  return (
    <div className="flex min-h-[40vh] items-center justify-center text-muted">
      <Spinner size={32} />
    </div>
  );
}
