import { useQuery } from '@tanstack/react-query';
import {
  comingUp,
  continueWatchingCard,
  dashboardCard,
  getContinueWatching,
  getDashboardStreams,
  getDisplayList,
  getHomeCalendar,
  homeKeys,
  isUpcoming,
  listShelfQuery,
  recentlyAired,
  titleCard,
  watchlistCard,
  type HomeCardModel,
  type HomeSettings,
  type PlannedShelf,
  type TrendingItem,
  type WatchlistItem,
  airsAt,
} from '@/api/home';

export interface ShelfData {
  cards: HomeCardModel[];
  total?: number;
  isLoading: boolean;
  error: unknown;
  refetch: () => void;
}

const CALENDAR_LIMIT = 40;
const UPCOMING_WINDOW_MS = 7 * 86_400_000;

/** Cards for one planned shelf; Continue Watching and the calendar are shared queries. */
export function useShelfCards(
  shelf: PlannedShelf,
  settings: HomeSettings,
  profileId: string,
): ShelfData {
  const kind = shelf.source.kind;
  const usesCW = kind === 'continueWatching' || kind === 'upcoming';
  const usesCalendar = kind === 'calendar' || kind === 'recentlyAired';
  const isList = kind === 'list' || kind === 'watchlist';

  const cw = useQuery({
    queryKey: homeKeys.continueWatching(profileId),
    queryFn: ({ signal }) => getContinueWatching(profileId, signal),
    enabled: usesCW,
    staleTime: 30_000,
  });
  const calendar = useQuery({
    queryKey: homeKeys.calendar(profileId),
    queryFn: ({ signal }) => getHomeCalendar(profileId, CALENDAR_LIMIT, signal),
    enabled: usesCalendar,
    staleTime: 5 * 60_000,
  });
  const listQuery =
    kind === 'watchlist'
      ? {
          source: 'watchlist',
          limit: String(shelf.cap),
          offset: '0',
          ...(settings.hideWatched ? { hideWatched: 'true' } : {}),
        }
      : listShelfQuery(shelf, settings);
  const list = useQuery({
    queryKey: homeKeys.shelf(profileId, shelf.id, listQuery),
    queryFn: async ({ signal }) => {
      const itemShape =
        kind === 'watchlist' || (shelf.source.kind === 'list' && shelf.source.shape === 'item');
      if (itemShape) {
        const page = await getDisplayList<WatchlistItem>(profileId, listQuery, signal);
        return { cards: page.items.map(watchlistCard), total: page.total };
      }
      const ranked = shelf.source.kind === 'list' && shelf.source.ranked;
      const page = await getDisplayList<TrendingItem>(profileId, listQuery, signal);
      return {
        cards: page.items
          .filter((i) => i?.title)
          .map((i, idx) => titleCard(i.title, ranked ? idx + 1 : undefined)),
        total: page.total,
      };
    },
    enabled: isList,
    staleTime: 5 * 60_000,
  });
  const dashboard = useQuery({
    queryKey: homeKeys.dashboard(profileId),
    queryFn: ({ signal }) => getDashboardStreams(signal),
    enabled: kind === 'dashboard',
    refetchInterval: 15_000,
  });

  const now = new Date();
  switch (shelf.source.kind) {
    case 'continueWatching': {
      const items = (cw.data ?? []).filter(
        (i) =>
          !isUpcoming(i, now) ||
          (!settings.excludeUpcomingFromContinue &&
            (airsAt(i.nextEpisode!)?.getTime() ?? 0) <= now.getTime() + UPCOMING_WINDOW_MS),
      );
      return {
        cards: items.slice(0, shelf.cap).map(continueWatchingCard),
        isLoading: cw.isPending,
        error: cw.error,
        refetch: () => void cw.refetch(),
      };
    }
    case 'upcoming': {
      const items = (cw.data ?? []).filter((i) => isUpcoming(i, now));
      const at = (i: (typeof items)[number]) => airsAt(i.nextEpisode!)?.getTime() ?? 0;
      items.sort((a, b) => at(a) - at(b));
      return {
        cards: items.slice(0, shelf.cap).map(continueWatchingCard),
        isLoading: cw.isPending,
        error: cw.error,
        refetch: () => void cw.refetch(),
      };
    }
    case 'calendar':
    case 'recentlyAired': {
      const items = calendar.data ?? [];
      const cards =
        shelf.source.kind === 'calendar'
          ? comingUp(items, shelf.cap, now)
          : recentlyAired(items, shelf.source.sources, shelf.cap, now);
      return {
        cards,
        isLoading: calendar.isPending,
        error: calendar.error,
        refetch: () => void calendar.refetch(),
      };
    }
    case 'dashboard':
      return {
        cards: (dashboard.data ?? []).flatMap((s) => dashboardCard(s) ?? []),
        isLoading: dashboard.isPending,
        error: dashboard.error,
        refetch: () => void dashboard.refetch(),
      };
    case 'list':
    case 'watchlist':
      return {
        cards: list.data?.cards ?? [],
        total: list.data?.total,
        isLoading: list.isPending,
        error: list.error,
        refetch: () => void list.refetch(),
      };
    default:
      return { cards: [], isLoading: false, error: null, refetch: () => {} };
  }
}
