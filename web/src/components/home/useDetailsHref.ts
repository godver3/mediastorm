import { useRouter } from '@tanstack/react-router';
import type { MouseEvent } from 'react';
import { detailsLink } from '@/lib/links';
import type { HomeCardModel } from '@/api/home';

/**
 * href + click handler for a card that opens details: a real URL (open in new tab,
 * copy link) that still navigates client-side on a plain click.
 */
export function useDetailsHref() {
  const router = useRouter();
  return (card: Pick<HomeCardModel, 'link' | 'season' | 'episode'>) => {
    if (!card.link) return {};
    const target = detailsLink(
      card.link,
      card.season && card.episode ? { season: card.season, episode: card.episode } : undefined,
    );
    const location = router.buildLocation(target);
    const href = router.history.createHref(location.publicHref);
    const onClick = (e: MouseEvent<HTMLElement>) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey)
        return;
      e.preventDefault();
      void router.navigate(target);
    };
    return { href, onClick };
  };
}
