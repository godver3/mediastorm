import { useEffect, useRef, type KeyboardEvent } from 'react';
import type { Episode, Season } from '@/api/details';
import { Button, Icon } from '@/components';
import { cn } from '@/lib/cn';
import { formatDate, formatRuntime, isUnaired, remainingLabel } from './format';
import { DetailIcon } from './icons';

export interface EpisodeListProps {
  seasons: Season[];
  season: number;
  onSelectSeason: (n: number) => void;
  isWatched: (e: Episode) => boolean;
  onPlay: (e: Episode) => void;
  onToggleWatched: (e: Episode) => void;
  onMarkSeason: (watched: boolean) => void;
  /** Episode to scroll to and highlight (deep link / up next). */
  highlight?: { season: number; episode: number };
  busy?: boolean;
}

/** Season tabs + the season's episodes (native DetailsView+Rows / MobileDetailsView+Episodes). */
export function EpisodeList({
  seasons,
  season,
  onSelectSeason,
  isWatched,
  onPlay,
  onToggleWatched,
  onMarkSeason,
  highlight,
  busy,
}: EpisodeListProps) {
  const current = seasons.find((s) => s.number === season) ?? seasons[0];
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const highlightRef = useRef<HTMLLIElement | null>(null);

  useEffect(() => {
    if (highlight && highlight.season === current?.number && highlightRef.current) {
      highlightRef.current.scrollIntoView?.({ block: 'center', behavior: 'smooth' });
    }
  }, [highlight, current?.number]);

  if (!current) return null;
  const aired = current.episodes.filter((e) => !isUnaired(e.airedDate));
  const allWatched = aired.length > 0 && aired.every(isWatched);

  const onTabKey = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const delta = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    if (!delta) return;
    e.preventDefault();
    const next = (index + delta + seasons.length) % seasons.length;
    tabRefs.current[next]?.focus();
    const s = seasons[next];
    if (s) onSelectSeason(s.number);
  };

  return (
    <section aria-labelledby="episodes-heading" className="px-4 sm:px-6 lg:px-10">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h2 id="episodes-heading" className="text-lg font-semibold tracking-tight">
          Episodes
        </h2>
        <Button
          variant="ghost"
          size="sm"
          disabled={!aired.length || busy}
          onClick={() => onMarkSeason(!allWatched)}
          leftIcon={<DetailIcon name={allWatched ? 'eye' : 'eyeCheck'} size={16} />}
        >
          {allWatched ? 'Mark season unwatched' : 'Mark season watched'}
        </Button>
      </div>
      <div
        role="tablist"
        aria-label="Seasons"
        className="no-scrollbar -mx-1 mb-5 flex gap-2 overflow-x-auto px-1 pb-1"
      >
        {seasons.map((s, i) => {
          const selected = s.number === current.number;
          return (
            <button
              key={s.id || s.number}
              ref={(el) => {
                tabRefs.current[i] = el;
              }}
              role="tab"
              type="button"
              aria-selected={selected}
              tabIndex={selected ? 0 : -1}
              onKeyDown={(e) => onTabKey(e, i)}
              onClick={() => onSelectSeason(s.number)}
              className={cn(
                'shrink-0 cursor-pointer rounded-full border px-4 py-1.5 text-sm font-medium whitespace-nowrap transition',
                'focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none',
                selected
                  ? 'border-transparent accent-soft'
                  : 'border-line text-muted hover:bg-surface hover:text-fg',
              )}
            >
              {s.number === 0 ? 'Specials' : s.name || `Season ${s.number}`}
            </button>
          );
        })}
      </div>
      <ul
        role="tabpanel"
        aria-label={current.name || `Season ${current.number}`}
        className="space-y-2"
      >
        {current.episodes.map((ep) => {
          const watched = isWatched(ep);
          const unaired = isUnaired(ep.airedDate);
          const resume = ep.resume?.eligible ? ep.resume : undefined;
          const pct = resume && resume.duration > 0 ? (resume.position / resume.duration) * 100 : 0;
          const isHighlight =
            highlight?.season === ep.seasonNumber && highlight?.episode === ep.episodeNumber;
          const meta = [
            unaired ? `Airs ${formatDate(ep.airedDate) ?? ''}` : formatDate(ep.airedDate),
            formatRuntime(ep.runtimeMinutes),
            resume ? remainingLabel(resume.position, resume.duration) : undefined,
          ].filter(Boolean);
          return (
            <li
              key={ep.id}
              ref={isHighlight ? highlightRef : undefined}
              className={cn(
                'group/ep flex flex-col gap-3 rounded-card p-2 transition sm:flex-row sm:gap-5 sm:p-3',
                'hover:bg-surface',
                isHighlight && 'bg-surface ring-1 ring-line-strong',
              )}
            >
              <button
                type="button"
                disabled={unaired}
                onClick={() => onPlay(ep)}
                aria-label={`Play episode ${ep.episodeNumber}: ${ep.name}`}
                className="relative aspect-video w-full shrink-0 cursor-pointer overflow-hidden rounded-xl bg-surface-strong ring-1 ring-line outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-default sm:w-56 lg:w-64"
              >
                {ep.image?.url ? (
                  <img
                    src={ep.image.url}
                    alt=""
                    loading="lazy"
                    className="h-full w-full object-cover"
                  />
                ) : (
                  <div className="flex h-full w-full items-center justify-center text-sm text-faint">
                    Episode {ep.episodeNumber}
                  </div>
                )}
                {!unaired && (
                  <span className="absolute inset-0 flex items-center justify-center bg-black/40 opacity-0 transition group-hover/ep:opacity-100 group-focus-within/ep:opacity-100">
                    <span className="flex h-12 w-12 items-center justify-center rounded-full bg-accent text-accent-contrast shadow-card">
                      <Icon name="play" size={20} />
                    </span>
                  </span>
                )}
                {watched && (
                  <span className="glass absolute top-2 right-2 flex h-7 w-7 items-center justify-center rounded-full text-success">
                    <DetailIcon name="check" size={16} />
                  </span>
                )}
                {pct > 0 && !watched && (
                  <span className="absolute inset-x-2 bottom-2 h-1 overflow-hidden rounded-full bg-white/25">
                    <span
                      className="block h-full rounded-full bg-accent"
                      style={{ width: `${pct}%` }}
                    />
                  </span>
                )}
              </button>
              <div className="min-w-0 flex-1 py-0.5">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h3 className="text-[15px] font-semibold">
                      <span className="text-muted">{ep.episodeNumber}.</span> {ep.name}
                    </h3>
                    {meta.length > 0 && (
                      <p className="mt-0.5 text-xs text-muted">{meta.join(' · ')}</p>
                    )}
                  </div>
                  {!unaired && (
                    <button
                      type="button"
                      onClick={() => onToggleWatched(ep)}
                      aria-pressed={watched}
                      aria-label={
                        watched
                          ? `Mark episode ${ep.episodeNumber} unwatched`
                          : `Mark episode ${ep.episodeNumber} watched`
                      }
                      title={watched ? 'Mark unwatched' : 'Mark watched'}
                      className={cn(
                        'inline-flex h-8 w-8 shrink-0 cursor-pointer items-center justify-center rounded-full transition',
                        'focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none',
                        watched
                          ? 'text-success hover:bg-surface'
                          : 'text-faint hover:bg-surface hover:text-fg',
                      )}
                    >
                      <DetailIcon name={watched ? 'eyeCheck' : 'eye'} size={18} />
                    </button>
                  )}
                </div>
                {ep.overview && (
                  <p className="mt-2 line-clamp-3 text-sm leading-relaxed text-muted sm:line-clamp-2">
                    {ep.overview}
                  </p>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
