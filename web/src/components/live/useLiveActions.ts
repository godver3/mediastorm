import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import {
  catchupStartQuery,
  fetchCatchup,
  liveStartErrorMessage,
  livePlaybackParams,
  liveStartQuery,
  startLive,
  type EPGProgram,
  type LiveChannel,
} from '@/api/live';
import {
  cancelRecording,
  deleteRule,
  recordingKeys,
  recordProgram,
  recordSeries,
  recordTimeBlock,
  updateRule,
  type Recording,
  type RecordingRule,
} from '@/api/recordings';
import { errorMessage } from '@/components';
import { openPlayback } from '@/lib/playback';

export interface Notice {
  tone: 'success' | 'danger';
  text: string;
}

/**
 * The current page (base path, /watch, view and filters) as the player's `returnTo`, so
 * Back from playback.html lands on the same Live TV / Recordings view.
 */
export const hereReturnPath = () => window.location.pathname + window.location.search;

/** Tune a channel (or replay a past programme) and hand off to playback.html. */
export function useTuneChannel(profileId: string, profileName?: string) {
  const [tuning, setTuning] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const tune = async (
    channel: LiveChannel,
    programTitle?: string,
    opts: { stremioStreamIndex?: number } = {},
  ) => {
    setTuning(channel.id);
    setError(null);
    try {
      const start = await startLive(
        liveStartQuery(channel, profileId, {
          programTitle,
          profileName,
          stremioStreamIndex: opts.stremioStreamIndex,
        }),
      );
      openPlayback({
        ...livePlaybackParams(channel, start),
        profileName,
        returnTo: hereReturnPath(),
      });
    } catch (err) {
      setError(liveStartErrorMessage(err));
      setTuning(null);
    }
  };
  /** Catch-up: the provider's archive of `program` from its start (native liveCatchup). */
  const replay = async (channel: LiveChannel, program: EPGProgram) => {
    setTuning(channel.id);
    setError(null);
    try {
      const archive = await fetchCatchup(profileId, channel, program);
      const start = await startLive(
        catchupStartQuery(channel, archive, program, profileId, profileName),
      );
      openPlayback({
        ...livePlaybackParams({ ...channel, url: archive.url }, start),
        title: `${program.title} · ${channel.name}`,
        profileName,
        returnTo: hereReturnPath(),
      });
    } catch (err) {
      setError(liveStartErrorMessage(err));
      setTuning(null);
    }
  };
  return { tune, replay, tuning, error, clearError: () => setError(null) };
}

type Action =
  | { kind: 'record'; program: EPGProgram; channel: LiveChannel }
  | { kind: 'series'; program: EPGProgram; channel: LiveChannel; newOnly: boolean }
  | { kind: 'block'; channel: LiveChannel; durationMs: number }
  | { kind: 'cancel'; recording: Recording }
  | { kind: 'setSeries'; rule: RecordingRule; newOnly: boolean }
  | { kind: 'cancelSeries'; rule: RecordingRule };

/** Scheduling actions from the guide (native LiveStore+Recordings). */
export function useRecordActions(profileId: string, onNotice: (n: Notice) => void) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (a: Action): Promise<string> => {
      switch (a.kind) {
        case 'record':
          await recordProgram(profileId, a.program, a.channel);
          return `Recording scheduled for ${a.program.title}.`;
        case 'series':
          await recordSeries(profileId, a.program.title, a.channel, a.newOnly);
          return `Series recording set for ${a.program.title}.`;
        case 'block':
          await recordTimeBlock(profileId, a.channel, a.durationMs);
          return `Recording ${a.channel.name} for the next ${Math.round(a.durationMs / 3_600_000)} hours.`;
        case 'cancel':
          await cancelRecording(a.recording.id);
          return a.recording.status === 'pending' ? 'Recording cancelled.' : 'Recording stopped.';
        case 'setSeries':
          await updateRule(profileId, a.rule.id, { newOnly: a.newOnly });
          return a.newOnly ? 'Now recording new episodes only.' : 'Now recording every airing.';
        case 'cancelSeries':
          await deleteRule(a.rule.id);
          return 'Series recording cancelled.';
      }
    },
    onSuccess: (text) => onNotice({ tone: 'success', text }),
    onError: (err) => onNotice({ tone: 'danger', text: errorMessage(err) }),
    onSettled: () => qc.invalidateQueries({ queryKey: recordingKeys.all(profileId) }),
  });
}
