import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import {
  abortableSleep,
  getDetailsPlaybackSettings,
  getPrequeueStatus,
  playbackKeys,
  startPrequeue,
  type PrequeueStatus,
} from '@/api/playback';
import { prequeueBody } from '@/features/playback/resolve';
import type { PlaybackTitleInfo } from '@/lib/playback';

export type WarmPrequeue =
  | { phase: 'off' }
  | { phase: 'resolving'; id?: string }
  | { phase: 'ready'; id: string; status: PrequeueStatus }
  | { phase: 'unavailable' };

/** One warm-up per title and profile every ten minutes (native PlaybackCoordinator.prewarm). */
const started = new Map<string, { id: Promise<string | undefined>; at: number }>();
const WARM_TTL = 10 * 60_000;

/** Test hook. */
export const resetWarmPrequeues = () => started.clear();

function startOnce(profileId: string, title: PlaybackTitleInfo) {
  const key = `${profileId}|${title.titleId}`;
  const prev = started.get(key);
  if (prev && Date.now() - prev.at < WARM_TTL) return prev.id;
  // No episode or start: the server targets the series' next-up episode.
  const id = startPrequeue(profileId, prequeueBody({ title }))
    .then((r) => r.prequeueId)
    .catch(() => undefined);
  started.set(key, { id, at: Date.now() });
  void id.then((v) => v === undefined && started.delete(key));
  return id;
}

/**
 * Starts resolving what the details page would play as soon as it opens, so Play starts
 * quickly and Audio & Subtitles can list the stream's tracks — only when the profile (or this
 * device) turned prequeue on: the server defaults `disablePrequeue` to true because warm-ups
 * upload uncached debrid magnets. Skipped while the title has a library copy (Play uses it).
 * Polls every 1.5 s for up to 3 minutes, as native watchWarm.
 */
export function useWarmPrequeue(
  profileId: string | null,
  title: PlaybackTitleInfo,
  opts: { ready: boolean; hasLibraryCopy: boolean },
) {
  const settings = useQuery({
    queryKey: playbackKeys.detailsSettings(profileId ?? ''),
    queryFn: ({ signal }) => getDetailsPlaybackSettings(profileId!, signal),
    enabled: !!profileId,
    staleTime: 5 * 60_000,
  });
  const enabled =
    !!profileId &&
    !!title.titleId &&
    opts.ready &&
    !opts.hasLibraryCopy &&
    settings.data?.prequeueOnOpen === true;
  const [warm, setWarm] = useState<WarmPrequeue>({ phase: 'off' });
  const titleRef = JSON.stringify([title.titleId, title.mediaType]);

  useEffect(() => {
    if (!enabled || !profileId) {
      setWarm({ phase: 'off' });
      return;
    }
    const controller = new AbortController();
    const { signal } = controller;
    setWarm({ phase: 'resolving' });
    void (async () => {
      const id = await startOnce(profileId, title);
      if (signal.aborted) return;
      if (!id) return setWarm({ phase: 'unavailable' });
      setWarm({ phase: 'resolving', id });
      const deadline = Date.now() + 180_000;
      while (Date.now() < deadline && !signal.aborted) {
        try {
          const status = await getPrequeueStatus(id, signal);
          if (status.status === 'ready' && status.streamPath)
            return setWarm({ phase: 'ready', id, status });
          if (status.status === 'failed' || status.status === 'expired') break;
        } catch {
          if (signal.aborted) return;
        }
        try {
          await abortableSleep(1500, signal);
        } catch {
          return;
        }
      }
      if (!signal.aborted) setWarm({ phase: 'unavailable' });
    })();
    return () => controller.abort();
    // `title` is identified by titleRef; its other fields don't change the request.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, profileId, titleRef]);

  return { warm, settings: settings.data };
}
