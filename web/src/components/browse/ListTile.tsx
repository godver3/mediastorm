import * as Menu from '@radix-ui/react-dropdown-menu';
import { Link } from '@tanstack/react-router';
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { Glyph } from './Glyph';
import type { LinkTarget } from './MediaGrid';

/** Stable hue from a string, so a list keeps its colour. */
export function hueFor(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 360;
  return h;
}

export interface TileMenuItem {
  label: string;
  onSelect: () => void;
  danger?: boolean;
}

export interface ListTileProps {
  title: string;
  subtitle?: ReactNode;
  /** Small label above the title ("Top 10", "Genre"). */
  kicker?: string;
  icon?: ReactNode;
  /** Router destination ({ to, params, search }). */
  link: LinkTarget;
  /** Seed for the gradient; defaults to the title. */
  seed?: string;
  menu?: TileMenuItem[];
  size?: 'md' | 'sm';
}

/** A list in the Lists catalog: gradient tile with title and count; optional ⋯ menu. */
export function ListTile({
  title,
  subtitle,
  kicker,
  icon,
  link,
  seed,
  menu,
  size = 'md',
}: ListTileProps) {
  const hue = hueFor(seed ?? title);
  return (
    <div className="group/tile relative min-w-0">
      <Link
        to={link.to as never}
        params={link.params as never}
        search={link.search as never}
        className={cn(
          'relative flex flex-col justify-end overflow-hidden rounded-card p-4 ring-1 ring-line outline-none transition duration-200',
          'hover:-translate-y-0.5 hover:shadow-card hover:ring-line-strong focus-visible:ring-2 focus-visible:ring-accent',
          size === 'md' ? 'aspect-[16/9]' : 'aspect-[2/1]',
        )}
        style={{
          backgroundImage: `radial-gradient(120% 120% at 100% 0%, hsl(${hue} 70% 45% / 0.55), transparent 60%), linear-gradient(135deg, hsl(${(hue + 40) % 360} 45% 22%), hsl(${hue} 35% 12%))`,
        }}
      >
        {icon && (
          <span className="absolute top-3.5 left-4 text-white/80" aria-hidden="true">
            {icon}
          </span>
        )}
        {kicker && (
          <span className="text-[11px] font-semibold tracking-wider text-white/60 uppercase">
            {kicker}
          </span>
        )}
        <span
          className={cn(
            'line-clamp-2 font-semibold text-white',
            size === 'md' ? 'text-lg leading-tight' : 'text-[15px] leading-snug',
          )}
        >
          {title}
        </span>
        {subtitle && <span className="mt-0.5 text-xs text-white/70">{subtitle}</span>}
      </Link>
      {menu && menu.length > 0 && (
        <Menu.Root>
          <Menu.Trigger
            aria-label={`Options for ${title}`}
            className={cn(
              'glass absolute top-2.5 right-2.5 flex h-8 w-8 cursor-pointer items-center justify-center rounded-full text-fg transition outline-none',
              'opacity-0 group-hover/tile:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100 max-sm:opacity-100',
              'focus-visible:ring-2 focus-visible:ring-accent',
            )}
          >
            <Glyph name="more" size={16} />
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content
              align="end"
              sideOffset={6}
              className="glass z-50 min-w-40 rounded-xl p-1 shadow-card"
            >
              {menu.map((m) => (
                <Menu.Item
                  key={m.label}
                  onSelect={m.onSelect}
                  className={cn(
                    'cursor-pointer rounded-lg px-3 py-2 text-sm outline-none data-highlighted:bg-surface-strong',
                    m.danger && 'text-danger',
                  )}
                >
                  {m.label}
                </Menu.Item>
              ))}
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      )}
    </div>
  );
}

export function TileGrid({ children, dense }: { children: ReactNode; dense?: boolean }) {
  return (
    <div
      className={cn(
        'grid gap-4',
        dense
          ? 'grid-cols-[repeat(auto-fill,minmax(160px,1fr))]'
          : 'grid-cols-[repeat(auto-fill,minmax(220px,1fr))]',
      )}
    >
      {children}
    </div>
  );
}

export function Section({
  title,
  action,
  children,
}: {
  title: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="mb-10">
      <div className="mb-4 flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  );
}
