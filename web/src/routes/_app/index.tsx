import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export const Route = createFileRoute('/_app/')({
  component: () => <PlaceholderPage title="Home" icon="home" />,
});
