import { useEffect, useState, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

/** How long a logo gets to load before the plain-text title is shown instead. */
const LOGO_GRACE_MS = 1500;

/**
 * A title's logo art, falling back to the plain-text name. While the logo (or the
 * metadata that would carry it, `pending`) is still loading, an invisible
 * placeholder holds the logo's space instead of flashing the text title first.
 */
export function TitleLogo({
  logoUrl,
  name,
  pending,
  imgClassName,
  placeholderClassName,
  text,
  className,
}: {
  logoUrl?: string;
  name: string;
  /** Metadata still loading: a logo may yet arrive. */
  pending?: boolean;
  imgClassName: string;
  /** Sizes the empty space held while waiting (the logo's height). */
  placeholderClassName: string;
  /** Renders the text fallback. */
  text: (name: string) => ReactNode;
  /** Classes for the logo's wrapper (e.g. letting it shrink in a flex column). */
  className?: string;
}) {
  const [loaded, setLoaded] = useState<string>();
  const [failed, setFailed] = useState<string>();
  const [expired, setExpired] = useState(false);
  const waiting = pending || (!!logoUrl && loaded !== logoUrl && failed !== logoUrl);

  useEffect(() => {
    setExpired(false);
    if (!waiting) return;
    const t = window.setTimeout(() => setExpired(true), LOGO_GRACE_MS);
    return () => window.clearTimeout(t);
    // Restart the grace period per logo.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [logoUrl, pending]);

  const showText = !logoUrl
    ? !pending || expired
    : failed === logoUrl || (expired && loaded !== logoUrl);
  if (showText) return <>{text(name)}</>;

  return (
    <div className={cn('relative', className, loaded !== logoUrl && placeholderClassName)}>
      {logoUrl && (
        <img
          key={logoUrl}
          src={logoUrl}
          alt={name}
          onLoad={() => setLoaded(logoUrl)}
          onError={() => setFailed(logoUrl)}
          className={cn(
            imgClassName,
            'transition-opacity duration-300',
            loaded === logoUrl ? 'opacity-100' : 'absolute opacity-0',
          )}
        />
      )}
      {loaded !== logoUrl && <span className="sr-only">{name}</span>}
    </div>
  );
}
