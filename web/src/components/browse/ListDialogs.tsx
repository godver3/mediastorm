import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState, type FormEvent } from 'react';
import {
  createCustomList,
  deleteCustomList,
  listsKeys,
  renameCustomList,
  type CustomList,
} from '@/api/lists';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { errorMessage } from '@/components/EmptyState';

/** Create (no `list`) or rename (`list`) a custom list. */
export function ListNameDialog({
  profileId,
  list,
  open,
  onOpenChange,
  onSaved,
}: {
  profileId: string;
  list?: CustomList;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved?: (list: CustomList) => void;
}) {
  const [name, setName] = useState(list?.name ?? '');
  useEffect(() => {
    if (open) setName(list?.name ?? '');
  }, [open, list?.name]);
  const queryClient = useQueryClient();
  const save = useMutation({
    mutationFn: (n: string) =>
      list ? renameCustomList(profileId, list.id, n) : createCustomList(profileId, n),
    onSuccess: (saved) => {
      void queryClient.invalidateQueries({ queryKey: listsKeys.custom(profileId) });
      onOpenChange(false);
      onSaved?.(saved);
    },
  });
  const trimmed = name.trim();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (trimmed) save.mutate(trimmed);
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) save.reset();
        onOpenChange(o);
      }}
      title={list ? 'Rename list' : 'New list'}
      size="sm"
    >
      <form onSubmit={submit} className="flex flex-col gap-4">
        <label className="flex flex-col gap-1.5 text-sm">
          <span className="text-muted">Name</span>
          <input
            autoFocus
            value={name}
            maxLength={80}
            onChange={(e) => setName(e.target.value)}
            placeholder="Friday movie night"
            className="h-10 rounded-xl border border-line bg-surface px-3 text-fg outline-none focus:ring-2 focus:ring-accent"
          />
        </label>
        {save.isError && (
          <p role="alert" className="text-sm text-danger">
            {errorMessage(save.error)}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="submit" disabled={!trimmed} loading={save.isPending}>
            {list ? 'Save' : 'Create'}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function DeleteListDialog({
  profileId,
  list,
  open,
  onOpenChange,
  onDeleted,
}: {
  profileId: string;
  list: CustomList | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDeleted?: () => void;
}) {
  const queryClient = useQueryClient();
  const del = useMutation({
    mutationFn: (id: string) => deleteCustomList(profileId, id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: listsKeys.custom(profileId) });
      onOpenChange(false);
      onDeleted?.();
    },
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) del.reset();
        onOpenChange(o);
      }}
      title="Delete list?"
      description={
        list ? `“${list.name}” and its titles will be removed. This can't be undone.` : undefined
      }
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            loading={del.isPending}
            onClick={() => list && del.mutate(list.id)}
          >
            Delete
          </Button>
        </>
      }
    >
      {del.isError && (
        <p role="alert" className="text-sm text-danger">
          {errorMessage(del.error)}
        </p>
      )}
    </Dialog>
  );
}
