import { useDocumentTitle } from '@/lib/documentTitle';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useRouter } from '@tanstack/react-router';
import { useMemo, useState, type MouseEvent, type ReactNode } from 'react';
import { episodeKey, primaryTrailer, type Episode, type LibraryVersion } from '@/api/details';
import {
  releaseTitle,
  startPrequeue,
  type SourceResult,
  type TrackOverrides,
} from '@/api/playback';
import {
  currentPreset,
  defaultPreset,
  getQualityPresets,
  offeredFor,
  copiesFor,
  libraryCopyRule,
  LIBRARY_PRESET,
  presetIdOf,
  qualityPresetKeys,
} from '@/api/qualityPresets';
import type { Title } from '@/api/titles';
import { useSessionState } from '@/auth/sessionStore';
import { Button, EmptyState, ErrorState, Icon, PosterCard, Shelf, Spinner } from '@/components';
import {
  titleInfo,
  playEpisode,
  resumeStart,
  episodeCode,
  usePlayTitle,
} from '@/features/playback/usePlayTitle';
import { prequeueBody } from '@/features/playback/resolve';
import { cn } from '@/lib/cn';
import { detailsLink, type DetailsSearch } from '@/lib/links';
import type { StartPoint } from '@/lib/playback';
import { CastRow } from './CastRow';
import { DetailsHero } from './DetailsHero';
import { EpisodeList } from './EpisodeList';
import { airedRegularEpisodes, orderedSeasons, watchState } from './format';
import { ListsDialog } from './ListsDialog';
import { MoreMenu } from './MoreMenu';
import { QualityPresetDialog } from './QualityPresetDialog';
import { ResumePrompt } from './ResumePrompt';
import { SourcePickerDialog } from './SourcePickerDialog';
import { TracksDialog, tracksSummary } from './TracksDialog';
import { TrailerDialog } from './TrailerDialog';
import { useDetails } from './useDetails';
import { VersionsDialog } from './VersionsDialog';
import {
  forgetWarmPrequeues,
  pickedPresetFor,
  setPickedPreset,
  useWarmPrequeue,
} from './useWarmPrequeue';

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

/** A version / source picked outside Play, waiting on Resume or Start over (native). */
type PendingPick = {
  episode?: Episode;
  library?: LibraryVersion;
  source?: SourceResult;
  resumeAt: NonNullable<StartPoint>;
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
  const [pendingPick, setPendingPick] = useState<PendingPick | null>(null);
  const [listsOpen, setListsOpen] = useState(false);
  const [tracksOpen, setTracksOpen] = useState(false);
  const [qualityOpen, setQualityOpen] = useState(false);
  // A quality preset picked here, bound to the title it was picked for (native pickPreset);
  // kept per profile and title (pickedPresetFor) so the page sends it again when reopened.
  const [pickedPreset, setPagePreset] = useState<{ titleId: string; id: string } | null>(null);
  // Audio & Subtitles picks, bound to the warm stream they were made for (native).
  const [picks, setPicks] = useState<{ streamPath: string; overrides: TrackOverrides } | null>(
    null,
  );

  const bundle = d.bundle.data;
  useDocumentTitle(d.title?.name ?? search.name ?? 'Details');
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

  // Quality presets offered for the title (servers with presets); none hides the picker. For a
  // series, asked for the episode Play targets ("Library copy" is offered per episode).
  const presetEpisode =
    !isMovie && primaryEpisode
      ? {
          season: primaryEpisode.seasonNumber,
          episode: primaryEpisode.episodeNumber,
          absolute: primaryEpisode.absoluteEpisodeNumber,
        }
      : undefined;
  const presets = useQuery({
    queryKey: qualityPresetKeys.title(
      profileId ?? '',
      info.titleId,
      info.mediaType,
      presetEpisode ? `${presetEpisode.season}:${presetEpisode.episode}` : undefined,
    ),
    queryFn: ({ signal }) => getQualityPresets(profileId!, { ...info, ...presetEpisode }, signal),
    // Asked with the IDs the details add (as playback sends them); a series also waits for
    // the episode Play targets.
    enabled: !!profileId && !!info.titleId && !!bundle,
    staleTime: 5 * 60_000,
  });
  const presetChoices = presets.data ?? null;
  // Whether library copies play is decided for the title (any of its episodes in a library, a
  // preset remembered for it), as the server does: the episode list plays other episodes too.
  const titlePresetsQuery = {
    queryKey: qualityPresetKeys.title(profileId ?? '', info.titleId, info.mediaType),
    queryFn: ({ signal }: { signal: AbortSignal }) => getQualityPresets(profileId!, info, signal),
    staleTime: 5 * 60_000,
  };
  const titlePresets = useQuery({
    ...titlePresetsQuery,
    enabled: !!profileId && !!info.titleId && !!bundle,
  });
  const queryClient = useQueryClient();
  const offeredPresets = presetChoices ? offeredFor(presetChoices, info.mediaType) : [];
  const pickedPresetId =
    pickedPreset?.titleId === info.titleId
      ? pickedPreset.id
      : profileId && info.titleId
        ? pickedPresetFor(profileId, info.titleId)
        : undefined;
  const activePresetId =
    pickedPresetId ??
    (presetChoices ? presetIdOf(currentPreset(presetChoices, info.mediaType)) : undefined);
  const activePresetName =
    offeredPresets.find((p) => p.id === activePresetId)?.name ??
    (presetChoices ? currentPreset(presetChoices, info.mediaType).name : undefined);

  // Play takes a library copy unless another preset is picked for the title (as the server
  // decides for the other apps); while the choices load, the library copy.
  const libraryRule = libraryCopyRule(titlePresets.data ?? null, info.mediaType, pickedPresetId);
  const playableCopies = (ep?: Episode) => copiesFor(versionsFor(ep), libraryRule);
  const primaryVersions = playableCopies(isMovie ? undefined : primaryEpisode);
  const { warm: warmState, settings: playSettings } = useWarmPrequeue(profileId, info, {
    ready: !!bundle && !d.library.isPending,
    hasLibraryCopy: primaryVersions.length > 0,
    qualityPreset: pickedPresetId,
  });
  // The warm-up, while it resolves or once it resolved the episode Play targets.
  const warm =
    warmState.phase === 'resolving'
      ? warmState
      : warmState.phase === 'ready' &&
          (isMovie ||
            (primaryEpisode &&
              warmState.status.targetEpisode?.seasonNumber === primaryEpisode.seasonNumber &&
              warmState.status.targetEpisode?.episodeNumber === primaryEpisode.episodeNumber))
        ? warmState
        : undefined;
  const warmStatus = warm?.phase === 'ready' ? warm.status : undefined;
  const currentPicks: TrackOverrides =
    warmStatus && picks?.streamPath === warmStatus.streamPath && picks ? picks.overrides : {};
  const warmId =
    warmState.phase === 'ready' || warmState.phase === 'resolving' ? warmState.id : undefined;

  const start = (opts: {
    episode?: Episode;
    start?: StartPoint;
    library?: LibraryVersion;
    source?: SourceResult;
    /** "Stream instead": not the library copy, also not from the server. */
    stream?: boolean;
  }) =>
    void play({
      title: info,
      episode: opts.episode ? playEpisode(opts.episode) : undefined,
      start: opts.start,
      library: opts.library,
      source: opts.source,
      noLibraryCopy: opts.stream,
      ...(opts.library || opts.source
        ? {}
        : {
            warmPrequeueId: warmId,
            tracks: Object.keys(currentPicks).length ? currentPicks : undefined,
            qualityPreset: pickedPresetId,
          }),
    });

  /** A pick outside Play: ask Resume / Start over when there is a resume point (native). */
  const pickThenAsk = (pick: Omit<PendingPick, 'resumeAt'>) => {
    const resumeAt = pick.episode ? resumeStart(pick.episode.resume) : primaryStart;
    if (resumeAt) setPendingPick({ ...pick, resumeAt });
    else start({ ...pick, start: undefined });
  };

  /** Play, preferring the library: one copy plays, several ask (native playPreferringLibrary). */
  const playPreferringLibrary = (episode: Episode | undefined, from: StartPoint) =>
    void (async () => {
      let rule = libraryRule;
      // The title's rule still loading: wait for it rather than play a copy the server would not.
      if (versionsFor(episode).length > 0 && titlePresets.isPending && profileId) {
        const c = await queryClient.fetchQuery(titlePresetsQuery).catch(() => null);
        rule = libraryCopyRule(c, info.mediaType, pickedPresetId);
      }
      const copies = copiesFor(versionsFor(episode), rule);
      if (copies.length > 1) {
        setVersionsState({ versions: copies, episode, start: from, fromPlay: true });
        return;
      }
      start({ episode, start: from, library: copies[0] });
    })();

  const onEpisodePlay = (ep: Episode) => playPreferringLibrary(ep, resumeStart(ep.resume));

  const trailer = primaryTrailer(bundle?.trailers);
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
      <div className="relative max-sm:basis-full">
        <Button
          size="lg"
          disabled={!canPlay}
          loading={d.bundle.isPending || busy}
          onClick={() => playPreferringLibrary(primaryEpisode, primaryStart)}
          leftIcon={<Icon name="play" size={18} />}
          className="min-w-[160px] whitespace-nowrap max-sm:w-full"
        >
          {label}
        </Button>
        {primaryStart && resumeFraction > 0 && (
          <div className="absolute top-full left-0 mt-1.5 flex items-center gap-2 text-xs whitespace-nowrap text-muted max-sm:static max-sm:mb-1">
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
          icon={<Icon name="restart" />}
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
          icon={<Icon name="drive" />}
        />
      )}
      {offeredPresets.length > 0 && (
        <ActionButton
          label={activePresetName ? `Quality: ${activePresetName}` : 'Quality'}
          onClick={() => setQualityOpen(true)}
          icon={<Icon name="quality" />}
        />
      )}
      <ActionButton
        label="Choose source"
        disabled={!canPlay}
        onClick={() => setSourcesFor({ episode: isMovie ? undefined : primaryEpisode })}
        icon={<Icon name="sources" />}
      />
      {warm && (
        <ActionButton
          label={warmStatus ? 'Audio & Subtitles' : 'Finding a source…'}
          disabled={!warmStatus}
          onClick={() => setTracksOpen(true)}
          icon={warmStatus ? <Icon name="audio" /> : <Spinner size={18} />}
        />
      )}
      {trailer && (
        <ActionButton
          label="Trailer"
          onClick={() => setTrailerOpen(true)}
          icon={<Icon name="film" />}
        />
      )}
      <ActionButton
        label={d.inWatchlist ? 'In watchlist' : 'Add to watchlist'}
        pressed={d.inWatchlist}
        disabled={!bundle || d.watchlist.isPending}
        onClick={() => d.watchlist.mutate(!d.inWatchlist)}
        icon={<Icon name={d.inWatchlist ? 'check' : 'plus'} />}
      />
      <ActionButton
        label="Lists"
        disabled={!bundle}
        onClick={() => setListsOpen(true)}
        icon={<Icon name="list" />}
      />
      {isMovie ? (
        <ActionButton
          label={d.movieWatched ? 'Watched' : 'Mark watched'}
          pressed={d.movieWatched}
          disabled={!bundle || d.movieWatchedMutation.isPending}
          onClick={() => d.movieWatchedMutation.mutate(!d.movieWatched)}
          icon={<Icon name={d.movieWatched ? 'eyeCheck' : 'eye'} />}
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
            icon={<Icon name={seriesWatch === 'complete' ? 'eyeCheck' : 'eye'} />}
          />
        )
      )}
      <MoreMenu
        hidden={d.isHidden}
        hiddenBusy={d.hiddenMutation.isPending}
        onToggleHidden={() =>
          d.hiddenMutation.mutate(!d.isHidden, {
            // Hiding leaves the page, as native does.
            onSuccess: (_r, hide) => {
              if (!hide) return;
              if (window.history.length > 1) router.history.back();
              else void navigate({ to: '/' });
            },
          })
        }
        orderings={isMovie ? undefined : bundle?.seriesDetails?.availableOrderings}
        activeOrdering={d.pickedOrdering ?? bundle?.seriesDetails?.activeOrdering}
        onPickOrdering={(type) => {
          const current = d.pickedOrdering ?? bundle?.seriesDetails?.activeOrdering;
          if (type === current) return;
          // Season numbers mean something else in the new ordering: start from Up Next's.
          void navigate({
            to: '/details',
            search: (prev) => ({
              ...(prev as DetailsSearch),
              season: undefined,
              episode: undefined,
            }),
            replace: true,
            resetScroll: false,
          });
          d.ordering.mutate(type);
        }}
      />
    </div>
  );

  const tracks =
    warmStatus && playSettings?.showSelectedTracks
      ? tracksSummary(warmStatus, currentPicks)
      : undefined;
  const fileName = warmStatus && playSettings?.showFilename ? releaseTitle(warmStatus) : undefined;
  const infoLoading = !!warm && !warmStatus && !!playSettings?.showFilename;
  const infoLine = (infoLoading || fileName || tracks) && (
    <div className="mt-3 space-y-1 text-sm text-muted" data-testid="details-info-line">
      {infoLoading && (
        <div className="flex items-center gap-2">
          <Spinner size={14} /> Finding a source…
        </div>
      )}
      {fileName && (
        <div className="truncate" title={fileName}>
          {fileName}
        </div>
      )}
      {tracks && (
        <div className="flex flex-wrap gap-x-6 gap-y-1">
          {tracks.audio && <span>Audio: {tracks.audio}</span>}
          <span>Subtitles: {tracks.subtitle ?? 'Off'}</span>
        </div>
      )}
    </div>
  );

  const errors = [
    d.watchlist.error,
    d.movieWatchedMutation.error,
    d.episodesWatched.error,
    d.hiddenMutation.error,
  ].filter(Boolean);

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
          <>
            {infoLine}
            {errors.length > 0 && (
              <p role="alert" className="mt-3 text-sm text-danger">
                {(errors[0] as Error).message}
              </p>
            )}
          </>
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
            qualityPreset: pickedPresetId,
          }}
          onPickSource={(r) => {
            const ep = sourcesFor.episode;
            setSourcesFor(null);
            pickThenAsk({ episode: ep, source: r });
          }}
          onPickVersion={(v) => {
            const ep = sourcesFor.episode;
            setSourcesFor(null);
            pickThenAsk({ episode: ep, library: v });
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
            // From Play: play from where Play would have; from Versions: ask (native pickVersion).
            if (s.fromPlay) start({ episode: s.episode, library: v, start: s.start });
            else pickThenAsk({ episode: s.episode, library: v });
          }}
          onStreamInstead={
            versionsState.fromPlay
              ? () => {
                  const s = versionsState;
                  setVersionsState(null);
                  start({ episode: s.episode, start: s.start, stream: true });
                }
              : undefined
          }
        />
      )}

      {pendingPick && (
        <ResumePrompt
          heading={
            pendingPick.episode
              ? `${info.name} · ${episodeCode(pendingPick.episode.seasonNumber, pendingPick.episode.episodeNumber)}`
              : info.name
          }
          resumeAt={pendingPick.resumeAt}
          clock={clock}
          onCancel={() => setPendingPick(null)}
          onChoose={(from) => {
            const p = pendingPick;
            setPendingPick(null);
            start({ episode: p.episode, library: p.library, source: p.source, start: from });
          }}
        />
      )}

      {profileId && listsOpen && (
        <ListsDialog
          profileId={profileId}
          target={d.target}
          open={listsOpen}
          onOpenChange={setListsOpen}
        />
      )}

      {presetChoices && activePresetId && (
        <QualityPresetDialog
          presets={offeredPresets}
          active={activePresetId}
          defaultId={presetIdOf(defaultPreset(presetChoices, info.mediaType))}
          open={qualityOpen}
          onOpenChange={setQualityOpen}
          onPick={(id) => {
            setQualityOpen(false);
            if (id === activePresetId) return;
            setPagePreset({ titleId: info.titleId, id });
            // Picking the library copy tells the server, which forgets a preset remembered for
            // the title (the copy itself plays from here without asking it).
            if (id === LIBRARY_PRESET && profileId) {
              const episode = !isMovie && primaryEpisode ? playEpisode(primaryEpisode) : undefined;
              void startPrequeue(profileId, {
                ...prequeueBody({ title: info, episode }),
                qualityPreset: LIBRARY_PRESET,
              }).catch(() => undefined);
            }
            // The server remembers the pick once a prequeue or playback sends it; until then,
            // and while it may still rank Auto with the old preset, this page sends the pick
            // itself. A warm-up ranked with the old preset must not be reused.
            if (profileId) {
              setPickedPreset(profileId, info.titleId, id);
              forgetWarmPrequeues(profileId, info.titleId);
            }
          }}
        />
      )}

      {warmStatus && (
        <TracksDialog
          status={warmStatus}
          picks={currentPicks}
          onPicksChange={(o) => setPicks({ streamPath: warmStatus.streamPath ?? '', overrides: o })}
          open={tracksOpen}
          onOpenChange={setTracksOpen}
          onPlay={() => {
            setTracksOpen(false);
            playPreferringLibrary(primaryEpisode, primaryStart);
          }}
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
        'group/act inline-flex h-12 w-12 cursor-pointer items-center justify-center rounded-full border transition',
        'focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none disabled:pointer-events-none disabled:opacity-50',
        pressed
          ? 'border-transparent accent-soft'
          : 'glass border-line text-fg hover:bg-surface-strong',
      )}
    >
      {icon}
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
