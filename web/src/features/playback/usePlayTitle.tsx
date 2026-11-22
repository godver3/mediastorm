import { useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import {
  detailsKeys,
  episodeKey,
  getDetailsBundle,
  libraryMatches,
  toLibraryVersions,
  type DetailsBundle,
  type DetailsTarget,
  type Episode,
  type LibraryVersion,
  type ResumeState,
} from '@/api/details';
import type { Title } from '@/api/titles';
import { useActiveProfile } from '@/auth/hooks';
import { sessionStore } from '@/auth/sessionStore';
import { errorMessage } from '@/components';
import { detailsSearch } from '@/lib/links';
import {
  detailsReturnPath,
  openPlayback,
  type PlaybackTitleInfo,
  type StartPoint,
} from '@/lib/playback';
import { VersionsDialog } from '@/features/details/VersionsDialog';
import { PlaybackOverlay, type PlaybackOverlayState } from './PlaybackOverlay';
import { resolvePlayback, type PlayEpisode, type PlayRequest } from './resolve';

/** The server's resume decision as a start point (native ResumeState.startPoint). */
export function resumeStart(resume?: ResumeState | null): StartPoint {
  if (!resume?.eligible) return undefined;
  if (resume.position > 0) return { seconds: resume.position };
  if (resume.percent > 0) return { percent: resume.percent };
  return undefined;
}

export const episodeCode = (season: number, episode: number) =>
  `S${String(season).padStart(2, '0')}E${String(episode).padStart(2, '0')}`;

export function titleInfo(t: Title | DetailsTarget, extra?: Partial<PlaybackTitleInfo>) {
  const isTitle = 'id' in t;
  const info: PlaybackTitleInfo = isTitle
    ? {
        mediaType: t.mediaType === 'movie' ? 'movie' : 'series',
        titleId: t.id,
        name: t.name,
        year: t.year,
        tmdbId: t.tmdbId,
        tvdbId: t.tvdbId,
        imdbId: t.imdbId,
        posterUrl: t.poster?.url,
        backdropUrl: t.backdrop?.url,
        logoUrl: t.logo?.url,
      }
    : {
        mediaType: t.mediaType,
        titleId: t.titleId,
        name: t.name,
        year: t.year,
        tmdbId: t.tmdbId,
        tvdbId: t.tvdbId,
        imdbId: t.imdbId,
        posterUrl: t.posterUrl,
        backdropUrl: t.backdropUrl,
      };
  return { ...info, ...extra };
}

export const playEpisode = (ep: Episode): PlayEpisode => ({
  seasonNumber: ep.seasonNumber,
  episodeNumber: ep.episodeNumber,
  name: ep.name,
  imageUrl: ep.image?.url,
  airedDate: ep.airedDate,
  absoluteEpisodeNumber: ep.absoluteEpisodeNumber,
  numbering: ep.numbering,
});

export interface PlayTitleOptions {
  season?: number;
  episode?: number;
  /** Explicit start; `null` = from the beginning; omitted = the server's resume point. */
  start?: StartPoint | null;
}

/**
 * Start playback from any page, the way the details page does:
 *
 * ```tsx
 * const { playTitle, overlay } = usePlayTitle();
 * <Button onClick={() => playTitle(title, { season: 1, episode: 4 })}>Play</Button>
 * {overlay}
 * ```
 *
 * `playTitle` reads the details bundle (cached under `detailsKeys.bundle`) for the
 * resume point, the up-next episode and episode metadata, prefers a library copy
 * when the profile has one, then resolves through the prequeue and hands off to
 * /watch/playback.html with a `returnTo` of the title's details page. `play` takes a
 * fully built `PlayRequest` (the details page uses it for versions / manual sources).
 * Render `overlay` once: it shows progress with Cancel, and failures.
 */
export function usePlayTitle() {
  const queryClient = useQueryClient();
  const { profile } = useActiveProfile();
  const [state, setState] = useState<PlaybackOverlayState>({ kind: 'idle' });
  const abortRef = useRef<AbortController | null>(null);
  const [choice, setChoice] = useState<{ req: PlayRequest; versions: LibraryVersion[] } | null>(
    null,
  );

  useEffect(() => () => abortRef.current?.abort(), []);

  const cancel = useCallback(() => {
    abortRef.current?.abort();
    abortRef.current = null;
    setState({ kind: 'idle' });
  }, []);

  const run = useCallback(
    async (
      label: string,
      imageUrl: string | undefined,
      build: (signal: AbortSignal) => Promise<PlayRequest | null>,
    ) => {
      const profileId = sessionStore.getState().profileId;
      if (!profileId) return;
      abortRef.current?.abort();
      const controller = new AbortController();
      abortRef.current = controller;
      setState({ kind: 'resolving', label, stage: 'Starting…', imageUrl });
      try {
        const req = await build(controller.signal);
        if (!req) {
          // The build asked the user something instead (several library copies).
          if (!controller.signal.aborted) setState({ kind: 'idle' });
          return;
        }
        const params = await resolvePlayback(req, {
          profileId,
          profileName: profile?.name,
          signal: controller.signal,
          onProgress: (p) => {
            if (controller.signal.aborted) return;
            setState((s) => (s.kind === 'resolving' ? { ...s, ...p, detail: p.detail } : s));
          },
        });
        if (controller.signal.aborted) return;
        openPlayback(params);
      } catch (err) {
        if (controller.signal.aborted) return;
        setState({ kind: 'error', label, message: errorMessage(err) });
      } finally {
        if (abortRef.current === controller) abortRef.current = null;
      }
    },
    [profile?.name],
  );

  /** Play a fully specified request. */
  const play = useCallback(
    (req: PlayRequest) => {
      const ep = req.episode;
      const label = ep
        ? `${req.title.name} · ${episodeCode(ep.seasonNumber, ep.episodeNumber)}`
        : req.title.name;
      const search = detailsSearch(
        { ...req.title, id: req.title.titleId },
        ep ? { season: ep.seasonNumber, episode: ep.episodeNumber } : undefined,
      );
      return run(label, ep?.imageUrl || req.title.backdropUrl, async () => ({
        returnTo: detailsReturnPath(search),
        ...req,
      }));
    },
    [run],
  );

  /** Play a title (optionally one episode) with resume and library copies resolved for you. */
  const playTitle = useCallback(
    (title: Title, opts: PlayTitleOptions = {}) => {
      const base = titleInfo(title);
      const wanted =
        opts.season !== undefined && opts.episode !== undefined
          ? { season: opts.season, episode: opts.episode }
          : undefined;
      const label = wanted
        ? `${base.name} · ${episodeCode(wanted.season, wanted.episode)}`
        : base.name;
      return run(label, base.backdropUrl, async (signal) => {
        const profileId = sessionStore.getState().profileId!;
        const target: DetailsTarget = { ...base };
        const bundle = await queryClient
          .fetchQuery({
            queryKey: detailsKeys.bundle(profileId, base.mediaType, base.titleId),
            queryFn: ({ signal: s }) => getDetailsBundle(profileId, target, s),
            staleTime: 30_000,
          })
          .catch((): DetailsBundle => ({}));
        if (signal.aborted) throw new DOMException('Aborted', 'AbortError');
        const req = requestFromBundle(base, bundle, wanted, opts.start);
        const versions = await libraryMatches(profileId, target, signal)
          .then(toLibraryVersions)
          .catch(() => null);
        const copies = !versions
          ? []
          : req.episode
            ? (versions.episodes[episodeKey(req.episode.seasonNumber, req.episode.episodeNumber)] ??
              [])
            : base.mediaType === 'movie'
              ? versions.movie
              : [];
        const search = detailsSearch(
          { ...req.title, id: req.title.titleId },
          req.episode
            ? { season: req.episode.seasonNumber, episode: req.episode.episodeNumber }
            : undefined,
        );
        req.returnTo = detailsReturnPath(search);
        // Several copies: ask which (with Stream instead), as the details page does.
        if (copies.length > 1) {
          setChoice({ req, versions: copies });
          return null;
        }
        if (copies[0]) req.library = copies[0];
        return req;
      });
    },
    [queryClient, run],
  );

  const overlay: ReactNode = (
    <>
      <PlaybackOverlay
        state={state}
        onCancel={cancel}
        onDismiss={() => setState({ kind: 'idle' })}
      />
      {choice && (
        <VersionsDialog
          versions={choice.versions}
          open
          onOpenChange={(o) => !o && setChoice(null)}
          onPick={(v) => {
            setChoice(null);
            void play({ ...choice.req, library: v });
          }}
          onStreamInstead={() => {
            setChoice(null);
            void play(choice.req);
          }}
        />
      )}
    </>
  );

  return { play, playTitle, cancel, state, overlay, busy: state.kind === 'resolving' };
}

/** Builds the request for a title from its details bundle (resume, episode metadata, art). */
export function requestFromBundle(
  base: PlaybackTitleInfo,
  bundle: DetailsBundle,
  wanted?: { season: number; episode: number },
  start?: StartPoint | null,
): PlayRequest {
  const t = bundle.movieDetails ?? bundle.seriesDetails?.title;
  const title: PlaybackTitleInfo = {
    ...base,
    posterUrl: base.posterUrl ?? t?.poster?.url,
    backdropUrl: base.backdropUrl ?? t?.backdrop?.url,
    logoUrl: base.logoUrl ?? t?.logo?.url,
    year: base.year ?? t?.year,
    imdbId: base.imdbId ?? t?.imdbId,
    tmdbId: base.tmdbId ?? t?.tmdbId,
    tvdbId: base.tvdbId ?? t?.tvdbId,
  };
  if (base.mediaType === 'movie') {
    return { title, start: start === null ? undefined : (start ?? resumeStart(bundle.resume)) };
  }
  const episodes = bundle.seriesDetails?.seasons.flatMap((s) => s.episodes) ?? [];
  const up = bundle.upNextEpisode;
  const sn = wanted?.season ?? up?.seasonNumber;
  const en = wanted?.episode ?? up?.episodeNumber;
  if (sn === undefined || en === undefined) return { title };
  const ep = episodes.find((e) => e.seasonNumber === sn && e.episodeNumber === en);
  const episode: PlayEpisode = ep
    ? playEpisode(ep)
    : {
        seasonNumber: sn,
        episodeNumber: en,
        name: !wanted ? up?.title : undefined,
        imageUrl: !wanted ? up?.image?.url : undefined,
        numbering: !wanted ? up?.numbering : undefined,
      };
  const resume = ep?.resume ?? (!wanted ? bundle.resume : undefined);
  return {
    title,
    episode,
    start: start === null ? undefined : (start ?? resumeStart(resume)),
  };
}
