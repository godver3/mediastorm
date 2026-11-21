import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export const Route = createFileRoute('/_app/lists')({
  component: () => <PlaceholderPage title="Lists" icon="list" />,
});
