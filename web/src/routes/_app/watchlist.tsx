import { createFileRoute } from '@tanstack/react-router';
import { useSessionState } from '@/auth/sessionStore';
import { BrowseView } from '@/components/browse/BrowseView';
import { validateBrowseSearch } from '@/lib/browseSearch';

/** The profile's watchlist (native WatchlistView + the Lists grid's sort/filter options). */
export const Route = createFileRoute('/_app/watchlist')({
  validateSearch: validateBrowseSearch,
  component: WatchlistPage,
});

function WatchlistPage() {
  const { profileId } = useSessionState();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <BrowseView
      profileId={profileId ?? ''}
      list={{ kind: 'watchlist' }}
      title="Watchlist"
      search={search}
      onSearch={(next) => void navigate({ search: next, replace: true })}
      emptyTitle="Your watchlist is empty"
      emptyDescription="Add movies and shows from their details page and they'll show up here."
    />
  );
}
