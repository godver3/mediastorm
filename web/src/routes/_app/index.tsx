import { useQuery } from '@tanstack/react-query';
import { createFileRoute } from '@tanstack/react-router';
import { useMemo } from 'react';
import {
  getHomeSettings,
  heroShelf,
  HERO_MAX_SLIDES,
  homeKeys,
  planShelves,
  type HomeSettings,
  type PlannedShelf,
} from '@/api/home';
import { useActiveProfile } from '@/auth/hooks';
import { EmptyState, ErrorState, Page } from '@/components';
import { HomeHero, HomeHeroSkeleton } from '@/components/home/HomeHero';
import { HomeShelfRow, ShelfSkeleton } from '@/components/home/HomeShelfRow';
import { ServicesShelf } from '@/components/home/ServicesShelf';
import { useShelfCards } from '@/components/home/useShelfCards';
import { WatchSomethingShelf } from '@/components/home/WatchSomethingShelf';

export const Route = createFileRoute('/_app/')({
  component: HomePage,
});

function HomePage() {
  const { profileId, profile } = useActiveProfile();
  const settings = useQuery({
    queryKey: homeKeys.settings(profileId ?? ''),
    queryFn: ({ signal }) => getHomeSettings(profileId!, signal),
    enabled: !!profileId,
    staleTime: 60_000,
  });
  const plan = useMemo(
    () => (settings.data ? planShelves(settings.data, profile) : []),
    [settings.data, profile],
  );
  const hero = useMemo(() => heroShelf(plan), [plan]);

  if (!profileId || settings.isPending) {
    return (
      <Page bleed className="-mt-2">
        <HomeHeroSkeleton />
        <ShelfSkeleton title="" landscape />
        <ShelfSkeleton title="" landscape={false} />
      </Page>
    );
  }
  if (settings.error) {
    return (
      <Page title="Home">
        <ErrorState error={settings.error} onRetry={() => void settings.refetch()} />
      </Page>
    );
  }
  if (plan.length === 0) {
    return (
      <Page title="Home">
        <EmptyState
          icon="home"
          title="Nothing on Home yet"
          description="No shelves are enabled for this profile. Turn some on in the admin's Home layout settings."
        />
      </Page>
    );
  }

  return (
    <Page bleed className="-mt-2">
      {hero && <Hero shelf={hero} settings={settings.data} profileId={profileId} />}
      <h1 className="sr-only">Home</h1>
      {plan
        .filter((s) => s.id !== hero?.id)
        .map((shelf) => (
          <ShelfSwitch
            key={shelf.id}
            shelf={shelf}
            settings={settings.data}
            profileId={profileId}
          />
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
      return <WatchSomethingShelf title={shelf.title} profileId={profileId} />;
    default:
      return <HomeShelfRow shelf={shelf} settings={settings} profileId={profileId} />;
  }
}
