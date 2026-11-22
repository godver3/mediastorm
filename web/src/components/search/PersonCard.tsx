import { Link } from '@tanstack/react-router';
import type { Title } from '@/api/titles';
import { artworkUrl } from '@/lib/artwork';
import { personIdFromTitle } from '@/api/search';

const initials = (name: string) =>
  name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]!.toUpperCase())
    .join('');

/** Round headshot card for a person search result; opens /person. */
export function PersonCard({ title, onOpen }: { title: Title; onOpen?: () => void }) {
  const id = personIdFromTitle(title);
  const img = artworkUrl(title.poster?.url, 'w342');
  const body = (
    <>
      <div className="relative mx-auto aspect-square w-full overflow-hidden rounded-full bg-surface-strong ring-1 ring-line transition duration-200 group-hover/card:-translate-y-1 group-hover/card:shadow-card group-hover/card:ring-line-strong group-focus-visible/card:ring-2 group-focus-visible/card:ring-accent">
        {img ? (
          <img src={img} alt="" loading="lazy" className="h-full w-full object-cover" />
        ) : (
          <div className="flex h-full w-full items-center justify-center text-2xl font-semibold text-muted">
            {initials(title.name)}
          </div>
        )}
      </div>
      <div className="mt-2 truncate text-center text-sm font-medium">{title.name}</div>
      {title.overview && (
        <div className="truncate text-center text-xs text-muted">{title.overview}</div>
      )}
    </>
  );
  const cls = 'group/card block w-full outline-none';
  if (!id) return <div className={cls}>{body}</div>;
  return (
    <Link
      to="/person"
      search={{ id, name: title.name }}
      className={cls}
      onClick={onOpen}
      aria-label={title.name}
    >
      {body}
    </Link>
  );
}
