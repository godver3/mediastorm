import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export interface SearchParams {
  q?: string;
}

export const Route = createFileRoute('/_app/search')({
  validateSearch: (search: Record<string, unknown>): SearchParams => ({
    q: typeof search.q === 'string' && search.q.trim() ? search.q : undefined,
  }),
  component: SearchPage,
});

function SearchPage() {
  const { q } = Route.useSearch();
  return (
    <PlaceholderPage title="Search" icon="search">
      {q && <p className="pb-8 text-center text-sm text-muted">Query: “{q}”</p>}
    </PlaceholderPage>
  );
}
