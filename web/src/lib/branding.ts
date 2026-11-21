import { useEffect } from 'react';
import { useQuery } from '@tanstack/react-query';
import { brandingKeys, getBranding } from '@/api/branding';
import { apiPath } from '@/api/basePath';
import type { PublicBranding } from '@/api/types';

type Branding = PublicBranding['branding'];

/**
 * Resolves a branding path against the server, as the native clients do
 * (PublicBranding.resolve): absolute http(s) URLs pass through; uploads are
 * stored as `/branding/images/<slot>?v=…`, which the server serves under `/api`.
 */
export function resolveBrandingUrl(path: string | undefined | null): string | undefined {
  const trimmed = path?.trim();
  if (!trimmed) return undefined;
  if (/^https?:\/\//i.test(trimmed)) return trimmed;
  const relative = trimmed.startsWith('/') ? trimmed : `/${trimmed}`;
  return apiPath(relative.startsWith('/api/') ? relative.slice(4) : relative);
}

const first = (...paths: (string | undefined)[]) =>
  paths.map(resolveBrandingUrl).find((url): url is string => Boolean(url));

export interface ResolvedBranding {
  /** Wide wordmark: loading logo, else the mobile logo (sign-in, sidebar). */
  logoUrl?: string;
  /** Profile picker art: TV home image, else the mobile logo (desktop web, as in Expo). */
  profilePickerUrl?: string;
  /** Square icon (favicon, collapsed sidebar). */
  iconUrl?: string;
}

export function resolveBranding(branding: Branding | undefined): ResolvedBranding {
  if (!branding) return {};
  return {
    logoUrl: first(branding.loadingLogoUrl, branding.homeMobileLogoUrl),
    profilePickerUrl: first(branding.homeTvImageUrl, branding.homeMobileLogoUrl),
    iconUrl: first(branding.webIconUrl),
  };
}

/** Public, pre-login branding (GET /api/branding), cached for the session. */
export function useBranding(): ResolvedBranding & { isLoading: boolean } {
  const query = useQuery({
    queryKey: brandingKeys.all,
    queryFn: ({ signal }) => getBranding(signal),
    staleTime: 30 * 60_000,
    gcTime: Infinity,
    retry: 1,
  });
  return { ...resolveBranding(query.data?.branding), isLoading: query.isLoading };
}

/** Points the favicon/apple-touch-icon at the uploaded web icon (busts browser caches via ?v). */
export function useBrandingFavicon() {
  const { iconUrl } = useBranding();
  useEffect(() => {
    if (!iconUrl) return;
    for (const rel of ['icon', 'apple-touch-icon']) {
      let link = document.head.querySelector<HTMLLinkElement>(`link[rel="${rel}"]`);
      if (!link) {
        link = document.createElement('link');
        link.rel = rel;
        document.head.appendChild(link);
      }
      link.href = iconUrl;
    }
  }, [iconUrl]);
}
