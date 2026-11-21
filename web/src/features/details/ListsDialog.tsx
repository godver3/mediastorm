import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { detailsKeys, listMemberships, watchlistUpsert, type DetailsTarget } from '@/api/details';
import {
  addToCustomList,
  createCustomList,
  isSetAside,
  listCustomLists,
  listsKeys,
  removeFromCustomList,
  type CustomList,
} from '@/api/lists';
import { Button, Dialog, errorMessage, Icon, Spinner } from '@/components';
import { cn } from '@/lib/cn';

/** Why `name` can't be a new list's name (native CustomListsStore.nameProblem). */
export function listNameProblem(name: string, lists: CustomList[]): string | undefined {
  const trimmed = name.trim();
  if (!trimmed) return 'Enter a name for the list.';
  if (trimmed.toLowerCase() === 'set aside')
    return '“Set Aside” is reserved for titles parked out of Continue Watching.';
  if (lists.some((l) => l.name.trim().toLowerCase() === trimmed.toLowerCase()))
    return `You already have a list called “${trimmed}”.`;
  return undefined;
}

/**
 * Manage Lists (native DetailsListsMenu / AddToListSheet): each of the profile's lists
 * toggles the title in place (checked while the list holds it); New list creates one
 * with the title in it. Set Aside is left out, as on native.
 */
export function ListsDialog({
  profileId,
  target,
  open,
  onOpenChange,
}: {
  profileId: string;
  target: DetailsTarget;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const qc = useQueryClient();
  const listsQuery = useQuery({
    queryKey: listsKeys.custom(profileId),
    queryFn: ({ signal }) => listCustomLists(profileId, signal),
    enabled: open,
  });
  const lists = (listsQuery.data ?? []).filter((l) => !isSetAside(l));
  const membershipKey = [
    ...listsKeys.all,
    profileId,
    'memberships',
    target.mediaType,
    target.titleId,
  ] as const;
  const memberships = useQuery({
    queryKey: membershipKey,
    queryFn: ({ signal }) => listMemberships(profileId, lists, target, signal),
    enabled: open && listsQuery.isSuccess,
  });
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string>();
  const [newName, setNewName] = useState('');
  const [creating, setCreating] = useState(false);

  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: [...listsKeys.all, profileId] });
    void qc.invalidateQueries({ queryKey: [...detailsKeys.all, profileId] });
  };

  const toggle = async (list: CustomList) => {
    const items = memberships.data;
    if (!items || busy.has(list.id)) return;
    const item = items[list.id];
    setError(undefined);
    setBusy((b) => new Set(b).add(list.id));
    // The check flips at once; a failure puts it back.
    qc.setQueryData(membershipKey, (old: typeof items | undefined) => {
      const next = { ...old };
      if (item) delete next[list.id];
      else next[list.id] = { id: target.titleId, mediaType: target.mediaType };
      return next;
    });
    try {
      if (item) await removeFromCustomList(profileId, list.id, item.mediaType, item.id);
      else await addToCustomList(profileId, list.id, watchlistUpsert(target));
      await refresh();
    } catch (err) {
      qc.setQueryData(membershipKey, items);
      setError(`Failed to update ${list.name}: ${errorMessage(err)}`);
    } finally {
      setBusy((b) => {
        const next = new Set(b);
        next.delete(list.id);
        return next;
      });
    }
  };

  const create = async (e: FormEvent) => {
    e.preventDefault();
    const problem = listNameProblem(newName, listsQuery.data ?? []);
    if (problem) return setError(problem);
    setError(undefined);
    setCreating(true);
    try {
      const list = await createCustomList(profileId, newName.trim());
      await addToCustomList(profileId, list.id, watchlistUpsert(target));
      setNewName('');
      await refresh();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setCreating(false);
    }
  };

  const loading = listsQuery.isPending || (lists.length > 0 && memberships.isPending);

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Lists"
      description={`Add ${target.name} to your lists, or take it off them.`}
      size="sm"
    >
      {loading ? (
        <div className="flex justify-center py-6">
          <Spinner />
        </div>
      ) : (
        <ul className="max-h-[50vh] space-y-1.5 overflow-y-auto">
          {lists.length === 0 && (
            <li className="py-2 text-sm text-muted">You don’t have any lists yet.</li>
          )}
          {lists.map((l) => {
            const member = !!memberships.data?.[l.id];
            return (
              <li key={l.id}>
                <button
                  type="button"
                  role="checkbox"
                  aria-checked={member}
                  disabled={busy.has(l.id)}
                  onClick={() => void toggle(l)}
                  className={cn(
                    'flex w-full cursor-pointer items-center gap-3 rounded-xl border px-3 py-2.5 text-left text-sm transition',
                    'focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none disabled:opacity-60',
                    member
                      ? 'border-transparent accent-soft'
                      : 'border-line bg-surface hover:bg-surface-strong',
                  )}
                >
                  <span
                    className={cn(
                      'flex h-5 w-5 shrink-0 items-center justify-center rounded-md border',
                      member ? 'border-transparent bg-accent text-accent-contrast' : 'border-line',
                    )}
                  >
                    {member && <Icon name="check" size={14} />}
                  </span>
                  <span className="min-w-0 flex-1 truncate font-medium">{l.name}</span>
                  {typeof l.itemCount === 'number' && (
                    <span className="text-xs text-muted">{l.itemCount}</span>
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      )}
      <form onSubmit={(e) => void create(e)} className="mt-4 flex gap-2">
        <input
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          placeholder="New list name"
          aria-label="New list name"
          maxLength={80}
          className="h-10 min-w-0 flex-1 rounded-xl border border-line bg-surface px-3 text-sm outline-none placeholder:text-faint focus:border-accent"
        />
        <Button type="submit" loading={creating} leftIcon={<Icon name="plus" size={16} />}>
          Create
        </Button>
      </form>
      {error && (
        <p role="alert" className="mt-3 text-sm text-danger">
          {error}
        </p>
      )}
    </Dialog>
  );
}
