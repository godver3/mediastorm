import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export const Route = createFileRoute('/_app/live')({
  component: () => <PlaceholderPage title="Live TV" icon="tv" />,
});
