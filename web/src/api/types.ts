/** Body of POST /api/auth/login and POST /api/auth/refresh (handlers/auth.go LoginResponse). */
export interface AuthSession {
  token: string;
  expiresAt: string;
  accountId: string;
  username: string;
  isMaster: boolean;
}

/** GET /api/users item (models/user.go, MarshalJSON adds hasPin/hasIcon). */
export interface UserProfile {
  id: string;
  accountId: string;
  name: string;
  color?: string;
  iconUrl?: string;
  hasPin: boolean;
  hasIcon: boolean;
  pinLength?: number;
  isKidsProfile: boolean;
  allowShareLinks: boolean;
  activityPrivacy?: string;
  traktAccountId?: string;
  plexAccountId?: string;
  mdblistAccountId?: string;
  simklAccountId?: string;
  scrobAccountId?: string;
  kidsMode?: string;
  createdAt: string;
  updatedAt: string;
}

/** POST /api/users/{id}/pin/verify (handlers/users.go VerifyPinResponse). */
export interface VerifyPinResponse {
  valid: boolean;
  unlockToken?: string;
  expiresAt?: string;
}

/** GET /api/branding (handlers/settings.go PublicBrandingResponse). */
export interface PublicBranding {
  branding: {
    homeTvImageUrl?: string;
    homeMobileLogoUrl?: string;
    settingsTvImageUrl?: string;
    webIconUrl?: string;
    loadingLogoUrl?: string;
  };
}

/** Subset of GET /api/users/{id}/settings used by the shell. Extend in src/api/settings.ts. */
export interface UserSettingsLite {
  display?: {
    appearance?: {
      accentColor?: string;
    };
  };
}
