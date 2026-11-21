import { useQuery } from '@tanstack/react-query';
import { createFileRoute } from '@tanstack/react-router';
import { useMemo } from 'react';
import {
  getHomeSettings,
  heroShelf,
  HERO_MAX_SLIDES,
  homeKeys,
  homeViewIds,
  homeViewLabel,
  planShelves,
  resolveHomeView,
  type HomeSettings,
  type PlannedShelf,
} from '@/api/home';
import { useActiveProfile } from '@/auth/hooks';
import { EmptyState, ErrorState, Page } from '@/components';
import { Segmented } from '@/components/Segmented';
import { HomeHero, HomeHeroSkeleton } from '@/components/home/HomeHero';
import { HomeShelfRow, ShelfSkeleton } from '@/components/home/HomeShelfRow';
import { ServicesShelf } from '@/components/home/ServicesShelf';
import { useShelfCards } from '@/components/home/useShelfCards';
import { WatchSomethingShelf } from '@/components/home/WatchSomethingShelf';

interface HomeSearch {
  /** Home view (native HomeViews): movies, shows or a custom page-… id; unset = All. */
  view?: string;
}

export const Route = createFileRoute('/_app/')({
  validateSearch: (raw: Record<string, unknown>): HomeSearch => ({
    view:
      typeof raw.view === 'string' && raw.view !== 'all' && /^[a-zA-Z0-9-]{1,90}$/.test(raw.view)
        ? raw.view
        : undefined,
  }),
  component: HomePage,
});

function HomePage() {
  const { profileId, profile } = useActiveProfile();
  const { view = 'all' } = Route.useSearch();
  const navigate = Route.useNavigate();
  const query = useQuery({
    queryKey: homeKeys.settings(profileId ?? ''),
    queryFn: ({ signal }) => getHomeSettings(profileId!, signal),
    enabled: !!profileId,
    staleTime: 60_000,
  });
  const resolved = useMemo(
    () => (query.data ? resolveHomeView(query.data, view) : undefined),
    [query.data, view],
  );
  const plan = useMemo(() => (resolved ? planShelves(resolved, profile) : []), [resolved, profile]);
  const viewIds = useMemo(() => homeViewIds(query.data?.views), [query.data?.views]);
  const switcher = resolved ? (
    <div className="pointer-events-none absolute inset-x-0 top-3 z-10 flex justify-center px-4">
      <Segmented
        className="glass pointer-events-auto max-w-full overflow-x-auto"
        size="sm"
        label="Home view"
        value={resolved.view}
        options={viewIds.map((id) => ({
          value: id,
          label: homeViewLabel(id, query.data?.views),
        }))}
        onChange={(v) =>
          void navigate({ search: { view: v === 'all' ? undefined : v }, replace: true })
        }
      />
    </div>
  ) : null;
  const hero = useMemo(() => heroShelf(plan), [plan]);

  if (!profileId || query.isPending) {
    return (
      <Page bleed className="-mt-2" documentTitle="Home">
        <HomeHeroSkeleton />
        <ShelfSkeleton title="" landscape />
        <ShelfSkeleton title="" landscape={false} />
      </Page>
    );
  }
  if (query.error || !resolved) {
    return (
      <Page title="Home">
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      </Page>
    );
  }
  if (plan.length === 0) {
    return (
      <Page bleed className="relative -mt-2 pt-16" documentTitle="Home">
        {switcher}
        <EmptyState
          icon="home"
          title="Nothing on Home yet"
          description={
            resolved.view === 'all'
              ? "No shelves are enabled for this profile. Turn some on in the admin's Home layout settings."
              : `The ${homeViewLabel(resolved.view, resolved.views)} view has no shelves.`
          }
        />
      </Page>
    );
  }

  return (
    <Page bleed className="relative -mt-2" documentTitle="Home">
      {switcher}
      {!hero && switcher && <div className="h-14" aria-hidden="true" />}
      {hero && <Hero shelf={hero} settings={resolved} profileId={profileId} />}
      <h1 className="sr-only">Home</h1>
      {plan
        .filter((s) => s.id !== hero?.id)
        .map((shelf) => (
          <ShelfSwitch key={shelf.id} shelf={shelf} settings={resolved} profileId={profileId} />
        ))}
    </Page>
  );
}

function Hero({
  shelf,
  settings,
  profileId,
}: {
  shelf: PlannedShelf;
  settings: HomeSettings;
  profileId: string;
}) {
  const heroPlan = useMemo(
    () => ({ ...shelf, cap: Math.max(shelf.cap, HERO_MAX_SLIDES) }),
    [shelf],
  );
  const data = useShelfCards(heroPlan, settings, profileId);
  if (data.isLoading) return <HomeHeroSkeleton />;
  return <HomeHero cards={data.cards.slice(0, HERO_MAX_SLIDES)} label={shelf.title} />;
}

function ShelfSwitch({
  shelf,
  settings,
  profileId,
}: {
  shelf: PlannedShelf;
  settings: HomeSettings;
  profileId: string;
}) {
  switch (shelf.source.kind) {
    case 'services':
      return (
        <ServicesShelf
          title={shelf.title}
          services={shelf.source.services}
          settings={settings}
          profileId={profileId}
          cap={shelf.cap}
        />
      );
    case 'watchSomething':
      return (
        <WatchSomethingShelf title={shelf.title} profileId={profileId} filter={settings.filter} />
      );
    default:
      return <HomeShelfRow shelf={shelf} settings={settings} profileId={profileId} />;
  }
}
