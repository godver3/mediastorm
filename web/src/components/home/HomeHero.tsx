import { useEffect, useState } from 'react';
import type { HomeCardModel } from '@/api/home';
import { artworkUrl } from '@/lib/artwork';
import { cn } from '@/lib/cn';
import { Icon } from '../icons';
import { IconButton } from '../IconButton';
import { TitleLogo } from '../TitleLogo';
import { Skeleton } from '../Spinner';
import { useDetailsHref } from './useDetailsHref';

const ADVANCE_MS = 8000;

/**
 * Home's top carousel (native MobileHomeCarousel / HomeCarousel): up to ten titles
 * of the carousel row with backdrop, logo and overview, stepping every 8 s while
 * the pointer isn't over it.
 */
export function HomeHero({ cards, label }: { cards: HomeCardModel[]; label: string }) {
  const slides = cards.filter((c) => c.backdropUrl);
  const [index, setIndex] = useState(0);
  const [paused, setPaused] = useState(false);
  const linkFor = useDetailsHref();
  const count = slides.length;
  const i = count ? index % count : 0;

  useEffect(() => {
    if (paused || count < 2) return;
    const t = window.setTimeout(() => setIndex((n) => (n + 1) % count), ADVANCE_MS);
    return () => window.clearTimeout(t);
  }, [index, paused, count]);

  if (!count) return null;
  const slide = slides[i]!;
  const link = linkFor(slide);
  const go = (d: number) => setIndex((n) => (n + d + count) % count);

  return (
    <section
      aria-roledescription="carousel"
      aria-label={label}
      className="group/hero relative isolate mb-2 h-[min(72vh,640px)] min-h-[420px] w-full overflow-hidden"
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
      onKeyDown={(e) => {
        if (e.key === 'ArrowRight') go(1);
        if (e.key === 'ArrowLeft') go(-1);
      }}
    >
      {slides.map((s, n) => (
        <img
          key={s.id}
          src={artworkUrl(s.backdropUrl, 'w1280')}
          alt=""
          aria-hidden
          loading={n === 0 ? 'eager' : 'lazy'}
          className={cn(
            'absolute inset-0 -z-10 h-full w-full object-cover object-[center_20%] transition-opacity duration-700',
            n === i ? 'opacity-100' : 'opacity-0',
          )}
        />
      ))}
      <div className="absolute inset-0 -z-10 bg-gradient-to-r from-bg via-bg/70 to-transparent max-md:via-bg/40" />
      <div className="absolute inset-0 -z-10 bg-gradient-to-t from-bg via-transparent to-bg/30" />

      <div
        key={slide.id}
        className="flex h-full max-w-2xl flex-col justify-end gap-3 px-4 pt-24 pb-14 sm:px-6 md:pl-20 lg:pr-10 lg:pb-20 [&>*]:shrink-0"
        aria-live={paused ? 'polite' : 'off'}
      >
        {slide.badge && (
          <span className="w-fit rounded-md accent-soft px-2 py-0.5 text-xs font-semibold tracking-wide uppercase">
            {slide.badge} in {label}
          </span>
        )}
        <TitleLogo
          logoUrl={slide.logoUrl ? artworkUrl(slide.logoUrl, 'w500') : undefined}
          name={slide.title}
          placeholderClassName="h-24 sm:h-32"
          imgClassName="min-h-12 max-h-24 w-auto max-w-[min(420px,80%)] object-contain object-left drop-shadow-[0_4px_24px_rgba(0,0,0,.6)] sm:max-h-32"
          text={(n) => (
            <h2 className="text-4xl font-bold tracking-tight text-balance sm:text-5xl">{n}</h2>
          )}
        />
        {slide.subtitle && <p className="text-sm font-medium text-muted">{slide.subtitle}</p>}
        {slide.overview && (
          <p className="line-clamp-3 max-w-xl text-sm leading-relaxed text-fg/85 sm:text-base">
            {slide.overview}
          </p>
        )}
        <div className="flex flex-wrap gap-3 pt-2">
          {link.href && (
            <a
              href={link.href}
              onClick={link.onClick}
              className="inline-flex h-12 items-center gap-2 rounded-xl bg-accent px-6 text-base font-medium text-accent-contrast shadow-[0_8px_24px_-12px_var(--accent)] transition hover:brightness-110 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
            >
              More info
              <Icon name="chevronRight" size={18} />
            </a>
          )}
        </div>
      </div>

      {count > 1 && (
        <>
          <IconButton
            icon="chevronLeft"
            label="Previous title"
            variant="glass"
            onClick={() => go(-1)}
            className="absolute top-1/2 left-3 -translate-y-1/2 opacity-0 transition group-hover/hero:opacity-100 focus-visible:opacity-100 max-md:hidden"
          />
          <IconButton
            icon="chevronRight"
            label="Next title"
            variant="glass"
            onClick={() => go(1)}
            className="absolute top-1/2 right-3 -translate-y-1/2 opacity-0 transition group-hover/hero:opacity-100 focus-visible:opacity-100 max-md:hidden"
          />
          <div className="absolute right-4 bottom-6 flex gap-1.5 sm:right-6 lg:right-10 lg:bottom-10">
            {slides.map((s, n) => (
              <button
                key={s.id}
                type="button"
                aria-label={`Show ${s.title}`}
                aria-current={n === i}
                onClick={() => setIndex(n)}
                className={cn(
                  'h-1.5 rounded-full transition-all',
                  n === i ? 'w-6 bg-accent' : 'w-1.5 bg-white/40 hover:bg-white/70',
                )}
              />
            ))}
          </div>
        </>
      )}
    </section>
  );
}

export function HomeHeroSkeleton() {
  return (
    <div className="relative mb-2 h-[min(72vh,640px)] min-h-[420px] w-full">
      <Skeleton className="absolute inset-0 rounded-none" />
      <div className="absolute bottom-14 left-4 flex w-full max-w-xl flex-col gap-4 sm:left-6 md:left-20 lg:bottom-20">
        <Skeleton className="h-20 w-72 rounded-lg" />
        <Skeleton className="h-4 w-40 rounded" />
        <Skeleton className="h-16 w-full rounded" />
        <Skeleton className="h-12 w-60 rounded-xl" />
      </div>
    </div>
  );
}
