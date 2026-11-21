import { createFileRoute } from '@tanstack/react-router';
import { LibraryShowPage } from '@/components/browse/LibraryShowPage';

interface ShowSearch {
  /** The show's title: there's no single-group call, so the page searches for it. */
  title?: string;
}

/** An unmatched library show's seasons and episodes (native LibraryShowView). */
export const Route = createFileRoute('/_app/libraries_/$libraryId_/show/$groupId')({
  validateSearch: (raw: Record<string, unknown>): ShowSearch => ({
    title: typeof raw.title === 'string' && raw.title.trim() ? raw.title : undefined,
  }),
  component: ShowRoute,
});

function ShowRoute() {
  const { libraryId, groupId } = Route.useParams();
  const { title } = Route.useSearch();
  return <LibraryShowPage libraryId={libraryId} groupId={groupId} title={title} />;
}
