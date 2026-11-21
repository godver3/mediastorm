import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export const Route = createFileRoute('/_app/settings')({
  component: () => <PlaceholderPage title="Settings" icon="settings" />,
});
