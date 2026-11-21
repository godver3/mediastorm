import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export const Route = createFileRoute('/_app/watchlist')({
  component: () => <PlaceholderPage title="Watchlist" icon="bookmark" />,
});
