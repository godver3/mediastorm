import { useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useEffect, useState } from 'react';
import { apiRequest } from '@/api/client';
import { askAIList, listKey } from '@/api/lists';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';

interface ServerSettings {
  metadata?: { aiApiKey?: string; geminiApiKey?: string };
}

/** Whether the server has an AI key (masked in /api/settings); false when unreadable. */
export function useHasAI() {
  return useQuery({
    queryKey: ['home', 'server-ai'],
    queryFn: async ({ signal }) => {
      try {
        const s = await apiRequest<ServerSettings>('/settings', { signal });
        return [s?.metadata?.aiApiKey, s?.metadata?.geminiApiKey].some((k) => !!k);
      } catch {
        return false;
      }
    },
    staleTime: 10 * 60_000,
  }).data;
}

const EXAMPLES = [
  'Cozy mysteries set in small towns',
  'Mind-bending sci-fi from the 90s',
  'Feel-good comedies under 100 minutes',
];

/**
 * Ask AI (native BrowseList.askAI): describe what you're in the mood for, then the server's
 * AI picks open as a list (`display-list?source=custom-ai&q=`).
 */
export function AskAIDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [prompt, setPrompt] = useState('');
  const navigate = useNavigate();
  useEffect(() => {
    if (open) setPrompt('');
  }, [open]);
  const trimmed = prompt.trim();
  const submit = (e: { preventDefault(): void }) => {
    e.preventDefault();
    if (!trimmed) return;
    onOpenChange(false);
    void navigate({ to: '/lists/$listKey', params: { listKey: listKey(askAIList(trimmed)) } });
  };
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Ask AI"
      description="Describe what you want to watch and get a list to pick from."
      size="sm"
    >
      <form onSubmit={submit} className="flex flex-col gap-4">
        <textarea
          autoFocus
          aria-label="What do you want to watch?"
          value={prompt}
          maxLength={300}
          rows={3}
          onChange={(e) => setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) submit(e);
          }}
          placeholder="Something like Knives Out, but funnier"
          className="resize-none rounded-xl border border-line bg-surface px-3 py-2.5 text-fg outline-none focus:ring-2 focus:ring-accent"
        />
        <div className="flex flex-wrap gap-2">
          {EXAMPLES.map((ex) => (
            <button
              key={ex}
              type="button"
              onClick={() => setPrompt(ex)}
              className="cursor-pointer rounded-full border border-line px-3 py-1 text-xs text-muted transition hover:border-line-strong hover:text-fg"
            >
              {ex}
            </button>
          ))}
        </div>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="submit" disabled={!trimmed}>
            Ask
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
