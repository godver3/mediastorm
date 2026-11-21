import type { MouseEventHandler, ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface MediaCardProps {
  title: string;
  /** Secondary line: year, "S1 · E4", channel name... */
  subtitle?: ReactNode;
  imageUrl?: string | null;
  /** 0..1 watch progress; bar hidden when undefined or <= 0. */
  progress?: number;
  /** Top-left badge (e.g. "4K", "NEW"). */
  badge?: ReactNode;
  /** Render as a link when href is set, otherwise a button. */
  href?: string;
  onClick?: MouseEventHandler<HTMLElement>;
  className?: string;
}

function CardShell({
  href,
  onClick,
  className,
  label,
  children,
}: {
  href?: string;
  onClick?: MouseEventHandler<HTMLElement>;
  className: string;
  label: string;
  children: ReactNode;
}) {
  if (href) {
    return (
      <a href={href} onClick={onClick} className={className} aria-label={label}>
        {children}
      </a>
    );
  }
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(className, 'text-left cursor-pointer')}
      aria-label={label}
    >
      {children}
    </button>
  );
}

function Card({
  aspect,
  width,
  overlayText,
  ...props
}: MediaCardProps & { aspect: string; width: string; overlayText?: boolean }) {
  const { title, subtitle, imageUrl, progress, badge, href, onClick, className } = props;
  const pct = progress && progress > 0 ? Math.min(1, progress) * 100 : 0;
  return (
    <CardShell
      href={href}
      onClick={onClick}
      label={title}
      className={cn('group/card block shrink-0 snap-start outline-none', width, className)}
    >
      <div
        className={cn(
          'relative overflow-hidden rounded-card bg-surface-strong ring-1 ring-line transition duration-200',
          'group-hover/card:-translate-y-1 group-hover/card:shadow-card group-hover/card:ring-line-strong',
          'group-focus-visible/card:ring-2 group-focus-visible/card:ring-accent',
          aspect,
        )}
      >
        {imageUrl ? (
          <img
            src={imageUrl}
            alt=""
            loading="lazy"
            className="h-full w-full object-cover transition duration-300 group-hover/card:scale-[1.03]"
          />
        ) : (
          <div className="flex h-full w-full items-center justify-center p-3 text-center text-sm text-faint">
            {title}
          </div>
        )}
        {badge && (
          <span className="glass absolute top-2 left-2 rounded-md px-1.5 py-0.5 text-[11px] font-semibold">
            {badge}
          </span>
        )}
        {overlayText && (
          <div className="absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/85 via-black/40 to-transparent p-3 pt-10 text-white">
            <div className="truncate text-sm font-semibold">{title}</div>
            {subtitle && <div className="truncate text-xs text-white/70">{subtitle}</div>}
          </div>
        )}
        {pct > 0 && (
          <div
            className="absolute inset-x-2 bottom-2 h-1 overflow-hidden rounded-full bg-white/25"
            role="progressbar"
            aria-valuenow={Math.round(pct)}
            aria-valuemin={0}
            aria-valuemax={100}
          >
            <div className="h-full rounded-full bg-accent" style={{ width: `${pct}%` }} />
          </div>
        )}
      </div>
      {!overlayText && (
        <div className="mt-2 px-0.5">
          <div className="truncate text-sm font-medium">{title}</div>
          {subtitle && <div className="truncate text-xs text-muted">{subtitle}</div>}
        </div>
      )}
    </CardShell>
  );
}

/** 2:3 poster card (movies/series). */
export function PosterCard(props: MediaCardProps) {
  return <Card {...props} aspect="aspect-[2/3]" width="w-[140px] sm:w-[160px] lg:w-[180px]" />;
}

export interface LandscapeCardProps extends MediaCardProps {
  /** Draw title/subtitle over the image instead of below it. */
  overlayText?: boolean;
}

/** 16:9 landscape card (continue watching, episodes, channels). */
export function LandscapeCard({ overlayText, ...props }: LandscapeCardProps) {
  return (
    <Card
      {...props}
      overlayText={overlayText}
      aspect="aspect-video"
      width="w-[260px] sm:w-[300px] lg:w-[340px]"
    />
  );
}
