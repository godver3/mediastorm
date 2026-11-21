import { Link } from '@tanstack/react-router';
import type { CastMember } from '@/api/titles';
import { Shelf } from '@/components';

const initials = (name: string) =>
  name
    .split(/\s+/)
    .slice(0, 2)
    .map((p) => p[0] ?? '')
    .join('')
    .toUpperCase();

/** Top-billed cast. A card searches for the person (there's no person page yet). */
export function CastRow({ cast }: { cast: CastMember[] }) {
  if (!cast.length) return null;
  return (
    <Shelf title="Cast">
      {cast.slice(0, 20).map((c) => (
        <Link
          key={`${c.id}-${c.character ?? ''}`}
          to="/search"
          search={{ q: c.name }}
          className="group/cast block w-[112px] shrink-0 snap-start rounded-card text-center outline-none sm:w-[128px]"
        >
          <div className="mx-auto aspect-square w-24 overflow-hidden rounded-full bg-surface-strong ring-1 ring-line transition group-hover/cast:ring-line-strong group-focus-visible/cast:ring-2 group-focus-visible/cast:ring-accent sm:w-28">
            {c.profileUrl ? (
              <img
                src={c.profileUrl}
                alt=""
                loading="lazy"
                className="h-full w-full object-cover transition duration-300 group-hover/cast:scale-105"
              />
            ) : (
              <div className="flex h-full w-full items-center justify-center text-lg font-semibold text-faint">
                {initials(c.name)}
              </div>
            )}
          </div>
          <div className="mt-2 truncate text-sm font-medium">{c.name}</div>
          {c.character && <div className="truncate text-xs text-muted">{c.character}</div>}
        </Link>
      ))}
    </Shelf>
  );
}
