import { useNavigate, useRouterState } from '@tanstack/react-router';
import { useEffect, useRef, useState } from 'react';
import { Icon } from '../icons';

/** Global search input. Enter navigates to /search?q=…; "/" focuses it. */
export function SearchBox() {
  const navigate = useNavigate();
  const current = useRouterState({
    select: (s) =>
      s.location.pathname.endsWith('/search') ? (s.location.search as { q?: string }).q : undefined,
  });
  const [value, setValue] = useState(current ?? '');
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (current !== undefined) setValue(current);
  }, [current]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (e.key === '/' && !target?.closest('input,textarea,[contenteditable]')) {
        e.preventDefault();
        input.current?.focus();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  return (
    <form
      role="search"
      className="relative w-full max-w-xl"
      onSubmit={(e) => {
        e.preventDefault();
        const q = value.trim();
        void navigate({ to: '/search', search: q ? { q } : {} });
      }}
    >
      <Icon
        name="search"
        size={18}
        className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-faint"
      />
      <input
        ref={input}
        type="search"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder="Search movies, shows, people…"
        aria-label="Search"
        className="h-10 w-full rounded-full border border-line bg-surface pr-10 pl-10 text-sm text-fg placeholder:text-faint outline-none transition focus:border-accent focus:bg-surface-strong"
      />
      <kbd className="pointer-events-none absolute top-1/2 right-3.5 -translate-y-1/2 rounded border border-line px-1.5 text-[11px] text-faint max-md:hidden">
        /
      </kbd>
    </form>
  );
}
