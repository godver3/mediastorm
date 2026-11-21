import { APP_PATH } from './src/lib/appPath';

/**
 * Dev server base (`/watch/`). Production builds use relative asset URLs (`./`)
 * so a server under a base path (`/media/watch/`) can load them; index.html sets
 * <base href> at runtime. WEB_BASE overrides the dev base.
 */
export const DEV_BASE = process.env.WEB_BASE ?? `${APP_PATH}/`;
