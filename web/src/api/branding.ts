import { apiRequest } from './client';
import type { PublicBranding } from './types';

export const brandingKeys = { all: ['branding'] as const };

export const getBranding = (signal?: AbortSignal) =>
  apiRequest<PublicBranding>('/branding', { signal, noAuthRecovery: true });
