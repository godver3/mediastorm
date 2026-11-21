import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router';
import { useEffect, useMemo, useState } from 'react';
import {
  cancelRecording,
  deleteRecording,
  displayTitle,
  fetchRecordings,
  FILTER_EMPTY,
  FILTER_LABELS,
  matchesFilter,
  RECORDING_FILTERS,
  recordingKeys,
  recordingPlaybackParams,
  sortRecordings,
  type Recording,
  type RecordingFilter,
} from '@/api/recordings';
import { useActiveProfile } from '@/auth/hooks';
import { Button, Dialog, EmptyState, ErrorState, errorMessage, Page, Skeleton } from '@/components';
import { Notice } from '@/components/live/LiveBits';
import { useNow } from '@/components/live/useLiveData';
import { RecordingRow } from '@/components/recordings/RecordingRow';
import { RulesPanel } from '@/components/recordings/RulesPanel';
import { cn } from '@/lib/cn';
import { openPlayback } from '@/lib/playback';

type Tab = RecordingFilter | 'rules';
const TABS: Tab[] = [...RECORDING_FILTERS, 'rules'];

export interface RecordingsSearch {
  tab?: Tab;
}

export function validateRecordingsSearch(raw: Record<string, unknown>): RecordingsSearch {
  return { tab: TABS.includes(raw.tab as Tab) && raw.tab !== 'all' ? (raw.tab as Tab) : undefined };
}

export const Route = createFileRoute('/_app/recordings')({
  validateSearch: validateRecordingsSearch,
  component: RecordingsPage,
});

function RecordingsPage() {
  const { profileId, profile } = useActiveProfile();
  if (!profileId) return null;
  return <RecordingsScreen profileId={profileId} profileName={profile?.name} />;
}

function RecordingsScreen({ profileId, profileName }: { profileId: string; profileName?: string }) {
  const { tab = 'all' } = Route.useSearch();
  const navigate = useNavigate({ from: '/recordings' });
  const qc = useQueryClient();
  const now = useNow(15_000);
  const [notice, setNotice] = useState<{ tone: 'success' | 'danger'; text: string } | null>(null);
  useEffect(() => {
    if (!notice) return;
    const id = window.setTimeout(() => setNotice(null), 4000);
    return () => window.clearTimeout(id);
  }, [notice]);
  const say = (text: string, danger?: boolean) =>
    setNotice({ tone: danger ? 'danger' : 'success', text });

  const list = useQuery({
    queryKey: recordingKeys.list(profileId),
    queryFn: ({ signal }) => fetchRecordings(profileId, signal),
    // Keep in-progress ones fresh (size, status).
    refetchInterval: (q) =>
      q.state.data?.some((r) => ['pending', 'starting', 'running'].includes(r.status))
        ? 20_000
        : false,
  });

  const [confirm, setConfirm] = useState<{
    kind: 'delete' | 'cancel';
    recording: Recording;
  } | null>(null);
  const act = useMutation({
    mutationFn: async (c: { kind: 'delete' | 'cancel'; recording: Recording }) => {
      if (c.kind === 'delete') await deleteRecording(c.recording.id);
      else await cancelRecording(c.recording.id);
      return c;
    },
    onSuccess: (c) =>
      say(
        c.kind === 'delete'
          ? 'Recording deleted.'
          : c.recording.status === 'pending'
            ? 'Recording cancelled.'
            : 'Recording stopped.',
      ),
    onError: (e) => say(errorMessage(e), true),
    onSettled: () => qc.invalidateQueries({ queryKey: recordingKeys.all(profileId) }),
  });

  const counts = useMemo(() => {
    const out = {} as Record<RecordingFilter, number>;
    for (const f of RECORDING_FILTERS)
      out[f] = list.data?.filter((r) => matchesFilter(f, r)).length ?? 0;
    return out;
  }, [list.data]);
  const visible = useMemo(
    () =>
      tab === 'rules' ? [] : sortRecordings((list.data ?? []).filter((r) => matchesFilter(tab, r))),
    [list.data, tab],
  );

  const play = (r: Recording) => openPlayback({ ...recordingPlaybackParams(r), profileName });

  return (
    <Page
      title="Recordings"
      subtitle={
        list.data
          ? `${counts.completed} completed · ${counts.upcoming} scheduled${counts.recording ? ` · ${counts.recording} recording now` : ''}`
          : undefined
      }
      actions={
        <Link to="/live" className="text-sm font-medium text-accent hover:underline">
          Open guide
        </Link>
      }
    >
      <div
        role="tablist"
        aria-label="Recordings"
        className="-mx-4 mb-5 flex gap-1 overflow-x-auto border-b border-line px-4 sm:mx-0 sm:px-0"
      >
        {TABS.map((t) => (
          <Link
            key={t}
            role="tab"
            aria-selected={tab === t}
            to="/recordings"
            search={{ tab: t === 'all' ? undefined : t }}
            replace
            className={cn(
              '-mb-px flex shrink-0 items-center gap-2 border-b-2 px-3 py-2.5 text-sm font-medium transition',
              tab === t ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg',
            )}
          >
            {t === 'rules' ? 'Rules' : FILTER_LABELS[t]}
            {t !== 'rules' && list.data && counts[t] > 0 && (
              <span className="rounded-full bg-surface-strong px-1.5 text-xs text-muted">
                {counts[t]}
              </span>
            )}
          </Link>
        ))}
      </div>

      {tab === 'rules' ? (
        <RulesPanel profileId={profileId} onNotice={say} />
      ) : list.isPending ? (
        <ul className="flex flex-col gap-3">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-[120px] rounded-2xl sm:h-[124px]" />
          ))}
        </ul>
      ) : list.isError ? (
        <ErrorState error={list.error} onRetry={() => void list.refetch()} />
      ) : !visible.length ? (
        <EmptyState
          icon="record"
          title={FILTER_EMPTY[tab]}
          description={
            tab === 'all' || tab === 'upcoming'
              ? 'Pick a programme in the Live TV guide and choose Record.'
              : undefined
          }
          action={
            tab === 'all' || tab === 'upcoming' ? (
              <Button variant="secondary" onClick={() => void navigate({ to: '/live' })}>
                Go to Live TV
              </Button>
            ) : undefined
          }
        />
      ) : (
        <ul className="flex flex-col gap-3">
          {visible.map((r) => (
            <RecordingRow
              key={r.id}
              recording={r}
              now={now}
              busy={act.isPending && act.variables?.recording.id === r.id}
              onPlay={play}
              onCancel={(rec) => setConfirm({ kind: 'cancel', recording: rec })}
              onDelete={(rec) => setConfirm({ kind: 'delete', recording: rec })}
            />
          ))}
        </ul>
      )}

      <Dialog
        open={!!confirm}
        onOpenChange={(o) => !o && setConfirm(null)}
        size="sm"
        title={
          confirm?.kind === 'delete'
            ? 'Delete recording?'
            : confirm?.recording.status === 'pending'
              ? 'Cancel recording?'
              : 'Stop recording?'
        }
        description={
          confirm
            ? confirm.kind === 'delete'
              ? `“${displayTitle(confirm.recording)}” and its file will be removed.`
              : confirm.recording.status === 'pending'
                ? `“${displayTitle(confirm.recording)}” won’t be recorded.`
                : `What has been captured of “${displayTitle(confirm.recording)}” is kept.`
            : undefined
        }
        footer={
          <>
            <Button variant="ghost" onClick={() => setConfirm(null)}>
              Keep
            </Button>
            <Button
              variant="danger"
              onClick={() => {
                if (confirm) act.mutate(confirm);
                setConfirm(null);
              }}
            >
              {confirm?.kind === 'delete'
                ? 'Delete'
                : confirm?.recording.status === 'pending'
                  ? 'Cancel recording'
                  : 'Stop'}
            </Button>
          </>
        }
      />
      {notice && <Notice {...notice} onClose={() => setNotice(null)} />}
    </Page>
  );
}
