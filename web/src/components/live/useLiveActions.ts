import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import {
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

/** Tune a channel and hand off to playback.html. */
export function useTuneChannel(profileId: string, profileName?: string) {
  const [tuning, setTuning] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const tune = async (channel: LiveChannel, programTitle?: string) => {
    setTuning(channel.id);
    setError(null);
    try {
      const start = await startLive(
        liveStartQuery(channel, profileId, { programTitle, profileName }),
      );
      openPlayback({ ...livePlaybackParams(channel, start), profileName });
    } catch (err) {
      setError(liveStartErrorMessage(err));
      setTuning(null);
    }
  };
  return { tune, tuning, error, clearError: () => setError(null) };
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
