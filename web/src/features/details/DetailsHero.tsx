import { useState, type ReactNode } from 'react';
import type { Title } from '@/api/titles';
import { Skeleton } from '@/components';
import { cn } from '@/lib/cn';
import { infoLine, ratingChips } from './format';

/**
 * Full-bleed backdrop with the logo (or title), info line, ratings, genres and
 * overview; `actions` render under it. On phones the art sits above the text.
 */
export function DetailsHero({
  title,
  fallbackName,
  seasons,
  actions,
  below,
}: {
  title?: Title;
  fallbackName: string;
  seasons?: number;
  actions?: ReactNode;
  below?: ReactNode;
}) {
  const [logoFailed, setLogoFailed] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const backdrop = title?.backdrop?.url ?? title?.backdrops?.[0]?.url;
  const name = title?.name || fallbackName;
  const info = title ? infoLine(title, { seasons }) : [];
  const ratings = ratingChips(title?.ratings);
  const logo = !logoFailed ? title?.logo?.url : undefined;

  return (
    <section className="relative isolate overflow-hidden">
      <div className="relative h-[56vw] max-h-[420px] w-full md:absolute md:inset-0 md:h-auto md:max-h-none">
        {backdrop ? (
          <img
            src={backdrop}
            alt=""
            className="h-full w-full object-cover object-[center_20%]"
            fetchPriority="high"
          />
        ) : (
          <div className="h-full w-full bg-[radial-gradient(ellipse_at_top_right,color-mix(in_srgb,var(--accent)_30%,transparent),transparent_60%)]" />
        )}
        <div className="absolute inset-0 bg-gradient-to-t from-bg via-bg/30 to-transparent md:via-bg/50" />
        <div className="absolute inset-0 hidden bg-gradient-to-r from-bg via-bg/75 to-transparent md:block" />
      </div>

      <div className="relative -mt-24 px-4 pb-8 sm:px-6 md:mt-0 md:flex md:min-h-[min(78vh,760px)] md:items-end md:pt-32 lg:px-10">
        <div className="max-w-2xl">
          {logo ? (
            <h1 className="mb-5">
              <img
                src={logo}
                alt={name}
                onError={() => setLogoFailed(true)}
                className="max-h-24 max-w-[75%] object-contain object-left drop-shadow-[0_4px_24px_rgb(0_0_0/0.5)] sm:max-h-32 md:max-w-[440px]"
              />
            </h1>
          ) : (
            <h1 className="mb-3 text-3xl font-bold tracking-tight text-balance sm:text-4xl lg:text-5xl">
              {name}
            </h1>
          )}

          {title ? (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-sm text-muted">
              {info.map((p, i) => (
                <span
                  key={p}
                  className={cn(i > 0 && "before:mr-3 before:text-faint before:content-['·']")}
                >
                  {p}
                </span>
              ))}
              {ratings.map((r) => (
                <span
                  key={r.source}
                  title={r.label}
                  className="inline-flex items-center gap-1.5 rounded-md border border-line bg-surface px-1.5 py-0.5 text-xs font-medium text-fg"
                >
                  <span className="text-faint">{shortLabel(r.source, r.label)}</span>
                  {r.text}
                </span>
              ))}
            </div>
          ) : (
            <Skeleton className="h-4 w-64" />
          )}

          {title?.genres && title.genres.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-1.5">
              {title.genres.slice(0, 4).map((g) => (
                <span
                  key={g}
                  className="rounded-full bg-surface-strong px-2.5 py-0.5 text-xs text-muted"
                >
                  {g}
                </span>
              ))}
            </div>
          )}

          {title ? (
            title.overview && (
              <div className="mt-4">
                <p
                  className={cn(
                    'max-w-prose text-[15px] leading-relaxed text-fg/85',
                    !expanded && 'line-clamp-4 md:line-clamp-3',
                  )}
                >
                  {title.overview}
                </p>
                {title.overview.length > 240 && (
                  <button
                    type="button"
                    onClick={() => setExpanded((e) => !e)}
                    className="mt-1 cursor-pointer text-sm font-medium text-muted hover:text-fg focus-visible:underline focus-visible:outline-none"
                  >
                    {expanded ? 'Less' : 'More'}
                  </button>
                )}
              </div>
            )
          ) : (
            <div className="mt-4 space-y-2">
              <Skeleton className="h-4 w-full max-w-xl" />
              <Skeleton className="h-4 w-11/12 max-w-xl" />
              <Skeleton className="h-4 w-2/3 max-w-md" />
            </div>
          )}

          {actions && <div className="mt-6">{actions}</div>}
          {below}
        </div>
      </div>
    </section>
  );
}

function shortLabel(source: string, label: string) {
  switch (source) {
    case 'tomatoes':
      return 'RT';
    case 'metacritic':
      return 'MC';
    case 'letterboxd':
      return 'LB';
    default:
      return label;
  }
}
