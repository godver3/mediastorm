import { useState } from 'react';
import {
  groupBackdrop,
  isShow,
  libraryPlayback,
  libraryResume,
  type LibraryGroup,
  type LibraryItem,
  type LibraryPlaybackInfo,
  type LibraryResume,
} from '@/api/library';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { artworkUrl } from '@/lib/artwork';
import { openPlayback } from '@/lib/playback';

export interface LibraryEpisodeInfo {
  seasonNumber: number;
  episodeNumber: number;
  episodeTitle?: string;
  image?: string;
}

interface Pending {
  group: LibraryGroup;
  episode?: LibraryEpisodeInfo;
  info: LibraryPlaybackInfo;
  resume: LibraryResume;
}

export const clock = (seconds: number) => {
  const s = Math.max(0, Math.floor(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const pad = (n: number) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
};

/**
 * Starting an unmatched library file (native LibraryFileStart): look up its stream, then
 * offer Resume from … / Start Over when it was left partway, else play from the start.
 */
export function useLibraryStart(profileId: string, profileName?: string) {
  const [starting, setStarting] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState<Pending | null>(null);

  const play = (p: Omit<Pending, 'resume'>, from?: number) => {
    const { group, episode: ep, info } = p;
    openPlayback({
      sourcePath: info.streamPath,
      title: ep?.episodeTitle ? `${group.title} · ${ep.episodeTitle}` : group.title,
      seriesTitle: isShow(group) ? group.title : undefined,
      episodeName: ep?.episodeTitle,
      seasonNumber: ep?.seasonNumber,
      episodeNumber: ep?.episodeNumber,
      mediaType: ep ? 'episode' : 'movie',
      titleId: group.id,
      year: group.year,
      headerImage: artworkUrl(ep?.image ?? groupBackdrop(group)),
      durationHint: info.durationSeconds,
      startOffset: from && from > 0 ? from : undefined,
      profileName,
    });
  };

  const start = async (group: LibraryGroup, item: LibraryItem, episode?: LibraryEpisodeInfo) => {
    setStarting(item.id);
    setError(null);
    try {
      const info = await libraryPlayback(profileId, item.id, profileName);
      const resume = await libraryResume(profileId, episode ? 'episode' : 'movie', info.streamPath);
      setStarting(null);
      if (resume) setPending({ group, episode, info, resume });
      else play({ group, episode, info });
    } catch (err) {
      setError(err);
      setStarting(null);
    }
  };

  const dialog = (
    <Dialog
      open={!!pending}
      onOpenChange={(o) => !o && setPending(null)}
      title={pending?.episode?.episodeTitle ?? pending?.group.title ?? ''}
      description={
        pending
          ? `Left at ${clock(pending.resume.position)}${
              pending.resume.duration > 0 ? ` of ${clock(pending.resume.duration)}` : ''
            }`
          : undefined
      }
      size="sm"
    >
      {pending && (
        <div className="flex flex-col gap-2">
          <Button
            onClick={() => {
              setPending(null);
              play(pending, pending.resume.position);
            }}
          >
            Resume from {clock(pending.resume.position)}
          </Button>
          <Button
            variant="secondary"
            onClick={() => {
              setPending(null);
              play(pending);
            }}
          >
            Start Over
          </Button>
        </div>
      )}
    </Dialog>
  );

  return { start, starting, error, dialog };
}
