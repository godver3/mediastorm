import { createFileRoute, useNavigate } from '@tanstack/react-router';
import { useEffect, useMemo, useRef, useState } from 'react';
import { guideKey, type ChannelQuery, type LiveChannel } from '@/api/live';
import { useActiveProfile } from '@/auth/hooks';
import { Button, EmptyState, ErrorState, Page, PageSpinner, Skeleton } from '@/components';
import { ChannelGrid } from '@/components/live/ChannelGrid';
import { GuideGrid } from '@/components/live/GuideGrid';
import { LiveActionDialog, type LiveActionTarget } from '@/components/live/LiveActionDialog';
import { KidsLiveUnavailable, Notice } from '@/components/live/LiveBits';
import { LiveToolbar, type LiveView } from '@/components/live/LiveToolbar';
import {
  useRecordActions,
  useTuneChannel,
  type Notice as NoticeT,
} from '@/components/live/useLiveActions';
import {
  currentIn,
  toggled,
  useActiveRecordings,
  useChannelLists,
  useChannels,
  useEpgStatus,
  useNow,
  useNowNext,
  useRecordingRules,
  useSchedules,
  useToggleChannelList,
} from '@/components/live/useLiveData';
import { GUIDE_HOURS, guideStart } from '@/lib/guide';

export interface LiveSearch {
  view?: LiveView;
  q?: string;
  /** Comma-separated categories. */
  cats?: string;
  fav?: boolean;
  source?: string;
  hidden?: boolean;
}

export function validateLiveSearch(raw: Record<string, unknown>): LiveSearch {
  const s = (v: unknown) => (typeof v === 'string' && v.trim() ? v : undefined);
  const b = (v: unknown) => v === true || v === 'true' || v === '1' || v === 1;
  return {
    view: raw.view === 'guide' || raw.view === 'channels' ? raw.view : undefined,
    q: s(raw.q),
    cats: s(raw.cats),
    fav: b(raw.fav) || undefined,
    source: s(raw.source),
    hidden: b(raw.hidden) || undefined,
  };
}

export const Route = createFileRoute('/_app/live')({
  validateSearch: validateLiveSearch,
  component: LivePage,
});

function LivePage() {
  const { profileId, profile, query } = useActiveProfile();
  if (!profileId) return null;
  // Wait for the profile so a kids profile never sends a Live TV request.
  if (!profile) return query.isPending ? <PageSpinner /> : null;
  if (profile.isKidsProfile) return <KidsLiveUnavailable title="Live TV" />;
  // Keyed by profile: a switch starts from fresh local state.
  return <LiveScreen key={profileId} profileId={profileId} profileName={profile.name} />;
}

function LiveScreen({ profileId, profileName }: { profileId: string; profileName?: string }) {
  const search = Route.useSearch();
  const navigate = useNavigate({ from: '/live' });
  const setSearch = (patch: Partial<LiveSearch>) =>
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });

  const now = useNow();
  const [start] = useState(() => guideStart(Date.now()));
  const [target, setTarget] = useState<LiveActionTarget | null>(null);
  const [notice, setNotice] = useState<NoticeT | null>(null);
  useEffect(() => {
    if (!notice) return;
    const id = window.setTimeout(() => setNotice(null), 4000);
    return () => window.clearTimeout(id);
  }, [notice]);

  const lists = useChannelLists(profileId);
  const favorites = useMemo(() => lists.data?.favorites ?? [], [lists.data]);
  const hiddenIds = useMemo(() => lists.data?.hidden ?? [], [lists.data]);
  const saveLists = useToggleChannelList(profileId);
  const epg = useEpgStatus(profileId);
  const epgEnabled = epg.data?.enabled === true;
  const view: LiveView = !epgEnabled && epg.isSuccess ? 'channels' : (search.view ?? 'guide');

  const selectedCats = useMemo(
    () => (search.cats ? search.cats.split(',').filter(Boolean) : []),
    [search.cats],
  );
  const query: ChannelQuery = {
    sourceId: search.source,
    categories: selectedCats,
    favoritesOnly: !!search.fav,
    search: search.q ?? '',
  };
  const channelsQ = useChannels(profileId, query, favorites);
  const pages = channelsQ.data?.pages;
  const allChannels = useMemo(() => pages?.flatMap((p) => p.channels) ?? [], [pages]);
  const channels = useMemo(
    () =>
      search.hidden || !hiddenIds.length
        ? allChannels
        : allChannels.filter((c) => !hiddenIds.includes(c.id)),
    [allChannels, hiddenIds, search.hidden],
  );
  const firstPage = pages?.[0];
  const categories = firstPage?.availableCategories ?? [];
  const sources = firstPage?.sources ?? [];

  const recordings = useActiveRecordings(profileId);
  const rules = useRecordingRules(profileId);
  const nowNext = useNowNext(profileId, channels, epgEnabled && view === 'channels');
  const { schedules, loading: scheduleLoading } = useSchedules(
    profileId,
    channels,
    start,
    GUIDE_HOURS,
    epgEnabled && view === 'guide',
  );

  const { tune, tuning, error: tuneError, clearError } = useTuneChannel(profileId, profileName);
  useEffect(() => {
    if (tuneError) {
      setNotice({ tone: 'danger', text: tuneError });
      clearError();
    }
  }, [tuneError, clearError]);
  const actions = useRecordActions(profileId, setNotice);

  const toggleFavorite = (c: LiveChannel) =>
    saveLists.mutate({ favorites: toggled(favorites, c.id), hidden: hiddenIds });
  const toggleHidden = (c: LiveChannel) =>
    saveLists.mutate({ favorites, hidden: toggled(hiddenIds, c.id) });
  const tuneChannel = (c: LiveChannel, programTitle?: string) => {
    setTarget(null);
    void tune(c, programTitle ?? currentIn(schedules[guideKey(c)], now)?.title);
  };

  // Infinite paging: a sentinel at the end loads the next page.
  const sentinel = useRef<HTMLDivElement>(null);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = channelsQ;
  useEffect(() => {
    const el = sentinel.current;
    if (!el || !hasNextPage || typeof IntersectionObserver === 'undefined') return;
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting) && !isFetchingNextPage) void fetchNextPage();
    });
    io.observe(el);
    return () => io.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage, view]);

  const footer = hasNextPage ? (
    <div ref={sentinel} className="flex justify-center p-4">
      <Button variant="secondary" loading={isFetchingNextPage} onClick={() => void fetchNextPage()}>
        Load more channels
      </Button>
    </div>
  ) : null;

  const total = firstPage?.total;
  const filtered = !!(search.q || search.fav || selectedCats.length || search.source);

  let body;
  if (channelsQ.isPending) {
    body = <LiveSkeleton view={view} />;
  } else if (channelsQ.isError) {
    body = <ErrorState error={channelsQ.error} onRetry={() => void channelsQ.refetch()} />;
  } else if (!channels.length) {
    body = filtered ? (
      <EmptyState
        icon="tv"
        title={search.fav ? 'No favorite channels' : 'No matching channels'}
        description={
          search.fav ? 'Star a channel to keep it here.' : 'Try a different search or category.'
        }
        action={
          <Button
            variant="secondary"
            onClick={() =>
              setSearch({ q: undefined, fav: undefined, cats: undefined, source: undefined })
            }
          >
            Clear filters
          </Button>
        }
      />
    ) : (
      <EmptyState
        icon="tv"
        title="No live channels"
        description="Add an M3U playlist, Xtream, HDHomeRun or Stremio source in the server settings to watch live TV."
      />
    );
  } else if (view === 'guide') {
    body = (
      <GuideGrid
        channels={channels}
        schedules={schedules}
        scheduleLoading={scheduleLoading}
        start={start}
        now={now}
        favorites={favorites}
        recordings={recordings.data ?? []}
        tuningId={tuning}
        onTune={(c) => tuneChannel(c)}
        onProgram={(channel, program) => setTarget({ channel, program })}
        onChannelMenu={(channel) => setTarget({ channel })}
        footer={footer}
      />
    );
  } else {
    body = (
      <>
        <ChannelGrid
          channels={channels}
          nowNext={nowNext}
          now={now}
          favorites={favorites}
          hidden={hiddenIds}
          recordings={recordings.data ?? []}
          tuningId={tuning}
          onTune={(c) => tuneChannel(c, nowNext[guideKey(c)]?.current?.title)}
          onChannelMenu={(channel) => setTarget({ channel })}
          onToggleFavorite={toggleFavorite}
        />
        {footer}
      </>
    );
  }

  const menuCurrent = target
    ? (currentIn(schedules[guideKey(target.channel)], now) ??
      nowNext[guideKey(target.channel)]?.current)
    : undefined;

  return (
    <Page
      title="Live TV"
      subtitle={
        total !== undefined
          ? `${total.toLocaleString()} channel${total === 1 ? '' : 's'}${epg.data && !epgEnabled ? ' · guide off' : ''}`
          : undefined
      }
    >
      <LiveToolbar
        view={view}
        guideAvailable={epgEnabled}
        search={search.q ?? ''}
        favoritesOnly={!!search.fav}
        categories={categories}
        selected={selectedCats}
        sources={sources}
        sourceId={search.source}
        showHidden={!!search.hidden}
        hiddenCount={hiddenIds.length}
        onView={(v) => setSearch({ view: v })}
        onSearch={(q) => setSearch({ q: q || undefined })}
        onFavorites={(on) => setSearch({ fav: on || undefined })}
        onCategories={(c) => setSearch({ cats: c.length ? c.join(',') : undefined })}
        onSource={(id) => setSearch({ source: id, cats: undefined })}
        onShowHidden={(on) => setSearch({ hidden: on || undefined })}
      />
      {body}
      <LiveActionDialog
        target={target}
        onClose={() => setTarget(null)}
        now={now}
        current={menuCurrent}
        recordings={recordings.data ?? []}
        rules={rules.data ?? []}
        favorite={!!target && favorites.includes(target.channel.id)}
        hidden={!!target && hiddenIds.includes(target.channel.id)}
        epgEnabled={epgEnabled}
        onTune={tuneChannel}
        onToggleFavorite={toggleFavorite}
        onToggleHidden={toggleHidden}
        actions={actions}
      />
      {notice && <Notice {...notice} onClose={() => setNotice(null)} />}
    </Page>
  );
}

function LiveSkeleton({ view }: { view: LiveView }) {
  if (view === 'channels') {
    return (
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
        {Array.from({ length: 12 }, (_, i) => (
          <Skeleton key={i} className="h-[92px] rounded-2xl" />
        ))}
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-2 rounded-2xl border border-line p-3">
      {Array.from({ length: 8 }, (_, i) => (
        <div key={i} className="flex gap-2">
          <Skeleton className="h-14 w-24 sm:w-52" />
          <Skeleton className="h-14 flex-1" />
        </div>
      ))}
    </div>
  );
}
