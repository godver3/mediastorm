import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export const Route = createFileRoute('/_app/libraries')({
  component: () => <PlaceholderPage title="Libraries" icon="library" />,
});
