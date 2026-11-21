import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';
import { validateDetailsSearch } from '@/lib/links';

export const Route = createFileRoute('/_app/details')({
  validateSearch: validateDetailsSearch,
  component: () => <PlaceholderPage title="Details" icon="library" />,
});
