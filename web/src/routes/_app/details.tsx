import { createFileRoute } from '@tanstack/react-router';
import { DetailsPage } from '@/features/details/DetailsPage';
import { validateDetailsSearch } from '@/lib/links';

export const Route = createFileRoute('/_app/details')({
  validateSearch: validateDetailsSearch,
  component: DetailsRoute,
});

function DetailsRoute() {
  const search = Route.useSearch();
  // Remount per title so toggles / dialogs never carry over between pages.
  return <DetailsPage key={`${search.mediaType}:${search.titleId}`} search={search} />;
}
