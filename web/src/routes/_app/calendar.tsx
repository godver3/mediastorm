import { createFileRoute } from '@tanstack/react-router';
import { PlaceholderPage } from '@/components/PlaceholderPage';

export const Route = createFileRoute('/_app/calendar')({
  component: () => <PlaceholderPage title="Calendar" icon="calendar" />,
});
