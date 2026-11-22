import { useEffect, useState } from 'react';
import {
  isYouTubeUrl,
  startTrailerPrequeue,
  trailerPrequeueStatus,
  youTubeEmbedUrl,
  type Trailer,
} from '@/api/details';
import { abortableSleep } from '@/api/playback';
import { apiPath } from '@/api/basePath';
import { sessionStore } from '@/auth/sessionStore';
import { Button, Dialog, Spinner } from '@/components';

type TrailerSource =
  | { kind: 'preparing' }
  | { kind: 'video'; url: string }
  | { kind: 'embed'; url: string }
  | { kind: 'none' };

/** The downloaded file; `<video>` sends no headers, so the token rides in the query (native). */
export function trailerServeUrl(id: string): string {
  const q = new URLSearchParams({ id });
  const token = sessionStore.getToken();
  if (token) q.set('token', token);
  return `${apiPath('/metadata/trailers/prequeue/serve')}?${q.toString()}`;
}

/**
 * A playable source for `trailer`, as native APIClient+Trailers.trailerURL gets one: a
 * YouTube trailer is downloaded by the server (`POST /metadata/trailers/prequeue`, polled
 * every second for up to two minutes) and played as an MP4; when that fails, the
 * privacy-enhanced YouTube embed plays it instead. Other URLs play as they are.
 */
export async function resolveTrailer(
  trailer: Trailer,
  signal?: AbortSignal,
): Promise<TrailerSource> {
  const embed = youTubeEmbedUrl(trailer);
  const fallback: TrailerSource = embed ? { kind: 'embed', url: embed } : { kind: 'none' };
  if (!isYouTubeUrl(trailer.url)) {
    return trailer.url ? { kind: 'video', url: trailer.url } : fallback;
  }
  try {
    const queued = await startTrailerPrequeue(trailer.url, signal);
    for (let i = 0; i < 120; i++) {
      const s = await trailerPrequeueStatus(queued.id, signal);
      if (s.status === 'ready') return { kind: 'video', url: trailerServeUrl(queued.id) };
      if (s.status === 'failed') return fallback;
      await abortableSleep(1000, signal);
    }
  } catch (err) {
    if (signal?.aborted) throw err;
  }
  return fallback;
}

/** Plays a trailer in a dialog: the server's download first, the YouTube embed as fallback. */
export function TrailerDialog({
  trailer,
  open,
  onOpenChange,
}: {
  trailer?: Trailer;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [source, setSource] = useState<TrailerSource>({ kind: 'preparing' });

  useEffect(() => {
    if (!open || !trailer) return;
    const controller = new AbortController();
    setSource({ kind: 'preparing' });
    resolveTrailer(trailer, controller.signal).then(setSource, () => {});
    return () => controller.abort();
  }, [open, trailer]);

  const embed = trailer ? youTubeEmbedUrl(trailer) : undefined;
  const useEmbed = () => {
    if (embed) setSource({ kind: 'embed', url: embed });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={trailer?.name || 'Trailer'}
      size="lg"
      className="max-w-5xl!"
    >
      <div className="relative aspect-video w-full overflow-hidden rounded-xl bg-black">
        {open && source.kind === 'preparing' && (
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 text-sm text-muted">
            <Spinner />
            <span>Preparing trailer…</span>
            {embed && (
              <Button variant="secondary" size="sm" onClick={useEmbed}>
                Play from YouTube instead
              </Button>
            )}
          </div>
        )}
        {open && source.kind === 'video' && (
          <video src={source.url} controls autoPlay className="h-full w-full" onError={useEmbed} />
        )}
        {open && source.kind === 'embed' && (
          <iframe
            src={source.url}
            title={trailer?.name || 'Trailer'}
            className="h-full w-full"
            allow="autoplay; encrypted-media; picture-in-picture; fullscreen"
            allowFullScreen
          />
        )}
        {open && source.kind === 'none' && (
          <div className="absolute inset-0 flex items-center justify-center text-sm text-muted">
            This trailer can’t be played here.
          </div>
        )}
      </div>
    </Dialog>
  );
}
