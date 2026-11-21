import { useEffect } from 'react';

export const APP_NAME = 'MediaStorm';

/** "Page · MediaStorm", or just the app name. */
export function formatDocumentTitle(title?: string | null): string {
  const t = title?.trim();
  return t ? `${t} · ${APP_NAME}` : APP_NAME;
}

/** Sets document.title while the calling page is mounted (undefined = app name only). */
export function useDocumentTitle(title?: string | null) {
  useEffect(() => {
    document.title = formatDocumentTitle(title);
  }, [title]);
}
