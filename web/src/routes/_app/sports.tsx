import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export const Route = createFileRoute('/_app/sports')({
  component: () => <PlaceholderPage title="Sports" icon="trophy" />,
});
