/**
 * Where the built app is served. Change this one value at cutover
 * (`/watch-next/` -> `/watch/`). Can also be overridden with WEB_BASE at build time.
 */
export const APP_BASE = process.env.WEB_BASE ?? '/watch-next/';
