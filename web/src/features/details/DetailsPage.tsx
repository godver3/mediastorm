import { useNavigate, useRouter } from '@tanstack/react-router';
import { useMemo, useState, type MouseEvent, type ReactNode } from 'react';
import { episodeKey, primaryTrailer, type Episode, type LibraryVersion } from '@/api/details';
import type { SourceResult } from '@/api/playback';
import type { Title } from '@/api/titles';
import { useSessionState } from '@/auth/sessionStore';
import { Button, EmptyState, ErrorState, Icon, PosterCard, Shelf } from '@/components';
import {
  titleInfo,
  playEpisode,
  resumeStart,
  episodeCode,
  usePlayTitle,
} from '@/features/playback/usePlayTitle';
import { cn } from '@/lib/cn';
import { detailsLink, type DetailsSearch } from '@/lib/links';
import type { StartPoint } from '@/lib/playback';
import { CastRow } from './CastRow';
import { DetailsHero } from './DetailsHero';
import { EpisodeList } from './EpisodeList';
import { airedRegularEpisodes, orderedSeasons, watchState } from './format';
import { DetailIcon } from './icons';
import { SourcePickerDialog } from './SourcePickerDialog';
import { TrailerDialog } from './TrailerDialog';
import { useDetails } from './useDetails';
import { VersionsDialog } from './VersionsDialog';

/** "1:02:14" / "23:14". */
export function clock(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

type VersionsState = {
  versions: LibraryVersion[];
  episode?: Episode;
  start?: StartPoint;
  fromPlay: boolean;
};

/**
 * Movie / series details (native Views/Details/DetailsView* and Mobile/MobileDetailsView*):
 * hero, Play / Resume (+ Start over), versions, manual source, trailer, watchlist and
 * watched toggles; seasons and episodes for series; cast, collection and More Like This.
 */
export function DetailsPage({ search }: { search: DetailsSearch }) {
  const { profileId } = useSessionState();
  const navigate = useNavigate();
  const router = useRouter();
  const d = useDetails(profileId ?? '', search);
  const { play, overlay, busy } = usePlayTitle();
  const [trailerOpen, setTrailerOpen] = useState(false);
  const [sourcesFor, setSourcesFor] = useState<{ episode?: Episode } | null>(null);
  const [versionsState, setVersionsState] = useState<VersionsState | null>(null);

  const bundle = d.bundle.data;
  const isMovie = search.mediaType === 'movie';
  const seasons = useMemo(() => orderedSeasons(bundle?.seriesDetails?.seasons ?? []), [bundle]);
  const allEpisodes = useMemo(() => seasons.flatMap((s) => s.episodes), [seasons]);

  // The episode Play targets: a deep-linked one, else the server's up next.
  const primaryEpisode = useMemo(() => {
    if (isMovie) return undefined;
    const want =
      search.season !== undefined && search.episode !== undefined
        ? { s: search.season, e: search.episode }
        : bundle?.upNextEpisode
          ? { s: bundle.upNextEpisode.seasonNumber, e: bundle.upNextEpisode.episodeNumber }
          : undefined;
    const found =
      want && allEpisodes.find((x) => x.seasonNumber === want.s && x.episodeNumber === want.e);
    return found ?? seasons.find((s) => s.number > 0)?.episodes[0] ?? allEpisodes[0];
  }, [isMovie, search.season, search.episode, bundle, allEpisodes, seasons]);

  const selectedSeason = search.season ?? primaryEpisode?.seasonNumber ?? seasons[0]?.number ?? 1;

  const primaryResume = isMovie
    ? bundle?.resume
    : (primaryEpisode?.resume ??
      (bundle?.upNextEpisode &&
      primaryEpisode?.seasonNumber === bundle.upNextEpisode.seasonNumber &&
      primaryEpisode?.episodeNumber === bundle.upNextEpisode.episodeNumber
        ? bundle.resume
        : undefined));
  const primaryStart = resumeStart(primaryResume);
  const resumeFraction =
    primaryResume?.eligible && primaryResume.duration > 0
      ? Math.min(1, primaryResume.position / primaryResume.duration)
      : primaryResume?.eligible
        ? primaryResume.percent / 100
        : 0;

  const title = d.title;
  const info = titleInfo(d.target, { logoUrl: title?.logo?.url });
  const versionsFor = (ep?: Episode) =>
    !d.library.data
      ? []
      : ep
        ? (d.library.data.episodes[episodeKey(ep.seasonNumber, ep.episodeNumber)] ?? [])
        : isMovie
          ? d.library.data.movie
          : [];

  const start = (opts: {
    episode?: Episode;
    start?: StartPoint;
    library?: LibraryVersion;
    source?: SourceResult;
  }) =>
    void play({
      title: info,
      episode: opts.episode ? playEpisode(opts.episode) : undefined,
      start: opts.start,
      library: opts.library,
      source: opts.source,
    });

  /** Play, preferring the library: one copy plays, several ask (native playPreferringLibrary). */
  const playPreferringLibrary = (episode: Episode | undefined, from: StartPoint) => {
    const copies = versionsFor(episode);
    if (copies.length > 1) {
      setVersionsState({ versions: copies, episode, start: from, fromPlay: true });
      return;
    }
    start({ episode, start: from, library: copies[0] });
  };

  const onEpisodePlay = (ep: Episode) => playPreferringLibrary(ep, resumeStart(ep.resume));

  const trailer = primaryTrailer(bundle?.trailers);
  const primaryVersions = versionsFor(isMovie ? undefined : primaryEpisode);
  const seriesWatch = watchState(airedRegularEpisodes(seasons), d.isEpisodeWatched);

  if (!search.titleId) {
    return (
      <EmptyState
        icon="library"
        title="Nothing to show"
        description="This link doesn’t name a title."
        className="min-h-[60vh]"
      />
    );
  }

  if (d.bundle.isError && !bundle) {
    return (
      <ErrorState
        title={`Couldn’t load ${search.name || 'this title'}`}
        error={d.bundle.error}
        onRetry={() => void d.bundle.refetch()}
        className="min-h-[60vh]"
      />
    );
  }

  const label = isMovie
    ? primaryStart
      ? 'Resume'
      : 'Play'
    : primaryEpisode
      ? `${primaryStart ? 'Resume' : 'Play'} ${episodeCode(primaryEpisode.seasonNumber, primaryEpisode.episodeNumber)}`
      : 'Play';
  const canPlay = isMovie ? !!bundle : !!primaryEpisode;

  const actions = (
    <div className="flex flex-wrap items-start gap-2 sm:gap-3">
      <div className="flex flex-col gap-1.5">
        <Button
          size="lg"
          disabled={!canPlay}
          loading={d.bundle.isPending || busy}
          onClick={() => playPreferringLibrary(primaryEpisode, primaryStart)}
          leftIcon={<Icon name="play" size={18} />}
          className="min-w-[160px] max-sm:flex-1"
        >
          {label}
        </Button>
        {primaryStart && resumeFraction > 0 && (
          <div className="flex items-center gap-2 text-xs whitespace-nowrap text-muted">
            <span className="h-1 w-full max-w-[90px] overflow-hidden rounded-full bg-surface-strong">
              <span
                className="block h-full rounded-full bg-accent"
                style={{ width: `${resumeFraction * 100}%` }}
              />
            </span>
            {'seconds' in primaryStart && <span>From {clock(primaryStart.seconds)}</span>}
          </div>
        )}
      </div>
      {primaryStart && (
        <ActionButton
          label="Start over"
          onClick={() => playPreferringLibrary(primaryEpisode, undefined)}
          icon={<DetailIcon name="restart" />}
        />
      )}
      {primaryVersions.length > 0 && (
        <ActionButton
          label={primaryVersions.length === 1 ? 'Version' : `${primaryVersions.length} versions`}
          onClick={() =>
            setVersionsState({
              versions: primaryVersions,
              episode: primaryEpisode,
              start: primaryStart,
              fromPlay: false,
            })
          }
          icon={<DetailIcon name="drive" />}
        />
      )}
      <ActionButton
        label="Choose source"
        disabled={!canPlay}
        onClick={() => setSourcesFor({ episode: isMovie ? undefined : primaryEpisode })}
        icon={<DetailIcon name="sources" />}
      />
      {trailer && (
        <ActionButton
          label="Trailer"
          onClick={() => setTrailerOpen(true)}
          icon={<DetailIcon name="film" />}
        />
      )}
      <ActionButton
        label={d.inWatchlist ? 'In watchlist' : 'Add to watchlist'}
        pressed={d.inWatchlist}
        disabled={!bundle || d.watchlist.isPending}
        onClick={() => d.watchlist.mutate(!d.inWatchlist)}
        icon={<DetailIcon name={d.inWatchlist ? 'check' : 'plus'} />}
      />
      {isMovie ? (
        <ActionButton
          label={d.movieWatched ? 'Watched' : 'Mark watched'}
          pressed={d.movieWatched}
          disabled={!bundle || d.movieWatchedMutation.isPending}
          onClick={() => d.movieWatchedMutation.mutate(!d.movieWatched)}
          icon={<DetailIcon name={d.movieWatched ? 'eyeCheck' : 'eye'} />}
        />
      ) : (
        seasons.length > 0 && (
          <ActionButton
            label={seriesWatch === 'complete' ? 'Watched' : 'Mark series watched'}
            pressed={seriesWatch === 'complete'}
            disabled={d.episodesWatched.isPending}
            onClick={() =>
              d.episodesWatched.mutate({
                episodes: airedRegularEpisodes(seasons),
                watched: seriesWatch !== 'complete',
                scope: 'show',
              })
            }
            icon={<DetailIcon name={seriesWatch === 'complete' ? 'eyeCheck' : 'eye'} />}
          />
        )
      )}
    </div>
  );

  const errors = [d.watchlist.error, d.movieWatchedMutation.error, d.episodesWatched.error].filter(
    Boolean,
  );

  const cardProps = (t: Title) => {
    const link = detailsLink(t);
    return {
      href: router.buildLocation(link).href,
      onClick: (e: MouseEvent<HTMLElement>) => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
        e.preventDefault();
        void navigate(link);
        window.scrollTo?.({ top: 0 });
      },
    };
  };

  const collection = d.collection.data;
  const similar = bundle?.similar ?? [];

  return (
    <div className="pb-16">
      <DetailsHero
        title={title}
        fallbackName={search.name}
        seasons={isMovie ? undefined : seasons.filter((s) => s.number > 0).length || undefined}
        actions={actions}
        below={
          errors.length > 0 && (
            <p role="alert" className="mt-3 text-sm text-danger">
              {(errors[0] as Error).message}
            </p>
          )
        }
      />

      <div className="space-y-8">
        {!isMovie && seasons.length > 0 && (
          <EpisodeList
            seasons={seasons}
            season={selectedSeason}
            onSelectSeason={(n) =>
              void navigate({
                to: '/details',
                search: (prev) => ({ ...(prev as DetailsSearch), season: n, episode: undefined }),
                replace: true,
                resetScroll: false,
              })
            }
            isWatched={d.isEpisodeWatched}
            onPlay={onEpisodePlay}
            onToggleWatched={(ep) =>
              d.episodesWatched.mutate({ episodes: [ep], watched: !d.isEpisodeWatched(ep) })
            }
            onMarkSeason={(watched) => {
              const s = seasons.find((x) => x.number === selectedSeason);
              if (s) d.episodesWatched.mutate({ episodes: s.episodes, watched, scope: 'season' });
            }}
            highlight={
              search.episode !== undefined && search.season !== undefined
                ? { season: search.season, episode: search.episode }
                : undefined
            }
            busy={d.episodesWatched.isPending}
          />
        )}
        {!isMovie && d.bundle.isPending && <EpisodesSkeleton />}

        {title?.credits?.cast && <CastRow cast={title.credits.cast} />}

        {collection && collection.movies.length > 1 && (
          <Shelf title={collection.name}>
            {collection.movies.map((m) => (
              <PosterCard
                key={m.id}
                title={m.name}
                subtitle={m.id === title?.id ? 'This movie' : m.year}
                imageUrl={m.poster?.url}
                {...cardProps(m)}
                className={cn(m.id === title?.id && 'opacity-70')}
              />
            ))}
          </Shelf>
        )}

        {similar.length > 0 && (
          <Shelf title="More like this">
            {similar.map((m) => (
              <PosterCard
                key={m.id}
                title={m.name}
                subtitle={m.year}
                imageUrl={m.poster?.url}
                {...cardProps(m)}
              />
            ))}
          </Shelf>
        )}
      </div>

      <TrailerDialog trailer={trailer} open={trailerOpen} onOpenChange={setTrailerOpen} />

      {sourcesFor && profileId && (
        <SourcePickerDialog
          profileId={profileId}
          open={!!sourcesFor}
          onOpenChange={(o) => !o && setSourcesFor(null)}
          target={{
            titleId: d.target.titleId,
            name: d.target.name,
            mediaType: d.target.mediaType,
            year: d.target.year,
            imdbId: d.target.imdbId,
            episode: sourcesFor.episode
              ? {
                  season: sourcesFor.episode.seasonNumber,
                  episode: sourcesFor.episode.episodeNumber,
                  numbering: sourcesFor.episode.numbering,
                }
              : undefined,
            versions: versionsFor(sourcesFor.episode),
          }}
          onPickSource={(r) => {
            const ep = sourcesFor.episode;
            setSourcesFor(null);
            start({ episode: ep, source: r, start: ep ? resumeStart(ep.resume) : primaryStart });
          }}
          onPickVersion={(v) => {
            const ep = sourcesFor.episode;
            setSourcesFor(null);
            start({ episode: ep, library: v, start: ep ? resumeStart(ep.resume) : primaryStart });
          }}
        />
      )}

      {versionsState && (
        <VersionsDialog
          versions={versionsState.versions}
          open={!!versionsState}
          onOpenChange={(o) => !o && setVersionsState(null)}
          onPick={(v) => {
            const s = versionsState;
            setVersionsState(null);
            start({ episode: s.episode, library: v, start: s.start });
          }}
          onStreamInstead={
            versionsState.fromPlay
              ? () => {
                  const s = versionsState;
                  setVersionsState(null);
                  start({ episode: s.episode, start: s.start });
                }
              : undefined
          }
        />
      )}

      {overlay}
    </div>
  );
}

function ActionButton({
  label,
  icon,
  onClick,
  pressed,
  disabled,
}: {
  label: string;
  icon: ReactNode;
  onClick: () => void;
  pressed?: boolean;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-pressed={pressed}
      aria-label={label}
      title={label}
      className={cn(
        'group/act inline-flex h-12 cursor-pointer items-center gap-2 rounded-xl border px-3.5 text-sm font-medium transition',
        'focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none disabled:pointer-events-none disabled:opacity-50',
        pressed
          ? 'border-transparent accent-soft'
          : 'glass border-line text-fg hover:bg-surface-strong',
      )}
    >
      {icon}
      <span className="hidden xl:inline">{label}</span>
    </button>
  );
}

function EpisodesSkeleton() {
  return (
    <div className="space-y-3 px-4 sm:px-6 lg:px-10" aria-hidden="true">
      <div className="flex gap-2">
        {[0, 1, 2].map((i) => (
          <div key={i} className="h-8 w-24 animate-pulse rounded-full bg-surface-strong" />
        ))}
      </div>
      {[0, 1, 2].map((i) => (
        <div key={i} className="flex gap-5 p-3">
          <div className="aspect-video w-56 animate-pulse rounded-xl bg-surface-strong" />
          <div className="flex-1 space-y-2 py-1">
            <div className="h-4 w-48 animate-pulse rounded bg-surface-strong" />
            <div className="h-3 w-full max-w-lg animate-pulse rounded bg-surface-strong" />
          </div>
        </div>
      ))}
    </div>
  );
}
