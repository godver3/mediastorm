import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export const Route = createFileRoute('/_app/recordings')({
  component: () => <PlaceholderPage title="Recordings" icon="record" />,
});
