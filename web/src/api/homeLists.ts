/**
 * Lists that come from Home's shelf config: a shelf's See all, the Lists tab's connected
 * lists, collection hubs, streaming services and "Because you watched" seeds
 * (native Views/Lists/ListsCatalog.swift, ShelfConfig.browseList, HomeSource).
 */
import {
  isMovieId,
  planShelves,
  remoteQuery,
  resolveServices,
  shelfListKey,
  shelfSource,
  type ContinueWatchingItem,
  type HomeSettings,
  type ShelfConfig,
} from './home';
import { parseListKey, type BrowseList, type ParsedListKey } from './lists';

type KidsProfile = Parameters<typeof planShelves>[1];

/** The BrowseList a shelf's See all opens (`shelf.<id>` keys). */
export function shelfBrowseList(
  shelfId: string,
  settings: HomeSettings,
  profile?: KidsProfile,
): BrowseList | null {
  // planShelves also adds the kids-list-N rows a curated kids profile gets.
  const planned = planShelves(settings, profile).find((p) => p.id === shelfId);
  const config = settings.shelves.find((c) => c.id === shelfId);
  const source = planned?.source ?? (config ? shelfSource(config) : undefined);
  if (!source || source.kind !== 'list') return null;
  const name = planned?.title ?? (config?.name || shelfId);
  return { kind: 'remote', id: shelfId, name, query: { ...source.query, name } };
}

/** One streaming service list (`service.<id>.<part>`), read from MDBList like the Home row. */
export function serviceBrowseList(
  serviceId: string,
  part: number,
  settings: HomeSettings,
): BrowseList | null {
  const config = settings.shelves.find((c) => c.id === 'streaming-services');
  const service = resolveServices(config?.streamingServices).find((s) => s.id === serviceId);
  const list = service?.lists[part] ?? service?.lists[0];
  if (!service || !list) return null;
  const name =
    service.lists.length > 1 ? `${service.name} · ${list.title ?? 'List'}` : service.name;
  return {
    kind: 'remote',
    id: `service-${service.id}-${part}`,
    name,
    query: { source: 'mdblist', name, url: list.url },
  };
}

export type ResolvedList = BrowseList | { kind: 'continue-watching' } | { kind: 'hidden' };

/**
 * A parsed `/lists/$listKey` with Home's config applied. `undefined` while settings load,
 * `null` when the shelf or service doesn't exist.
 */
export function resolveListKey(
  parsed: ParsedListKey,
  settings: HomeSettings | undefined,
  profile?: KidsProfile,
): ResolvedList | null | undefined {
  switch (parsed.kind) {
    case 'shelf':
      return settings ? shelfBrowseList(parsed.shelfId, settings, profile) : undefined;
    case 'service':
      return settings ? serviceBrowseList(parsed.serviceId, parsed.part, settings) : undefined;
    default:
      return parsed;
  }
}

export interface CatalogTile {
  key: string;
  title: string;
  kicker?: string;
  imageUrl?: string;
  logoUrl?: string;
}

const SOURCE_NAMES: Record<string, string> = {
  mdblist: 'MDBList',
  'trakt-list': 'Trakt',
  'simkl-list': 'Simkl',
  'letterboxd-list': 'Letterboxd',
  stremio: 'Stremio',
  'tmdb-list': 'TMDB',
  'publicmetadb-list': 'PublicMetaDB',
};

/** Lists configured as Home shelves (MDBList, Trakt, Simkl, Stremio…), enabled or not. */
export function connectedTiles(shelves: ShelfConfig[]): CatalogTile[] {
  return shelves.flatMap((c) => {
    const q = remoteQuery(c);
    if (!q) return [];
    return [
      { key: `shelf.${c.id}`, title: c.name || c.id, kicker: SOURCE_NAMES[q.source!] ?? 'List' },
    ];
  });
}

/** Collection hubs: each card opens another shelf's list, with the card's own art. */
export function hubSections(shelves: ShelfConfig[]): { title: string; tiles: CatalogTile[] }[] {
  const byId = new Map(shelves.map((c) => [c.id, c]));
  return shelves
    .filter((c) => c.type === 'collection-hub')
    .flatMap((hub) => {
      const tiles = [...(hub.collectionItems ?? [])]
        .filter((i) => i.enabled !== false)
        .sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
        .flatMap((item): CatalogTile[] => {
          const src = item.sourceShelfId ? byId.get(item.sourceShelfId) : undefined;
          const source = src ? shelfSource(src) : undefined;
          const key = src && source ? shelfListKey(src, source) : undefined;
          if (!src || !key || !parseListKey(key)) return [];
          return [
            {
              key,
              title: item.name || src.name || src.id,
              imageUrl: item.heroArtUrl,
              logoUrl: item.heroArtUrl ? undefined : item.logoUrl,
            },
          ];
        });
      return tiles.length ? [{ title: hub.name || hub.id, tiles }] : [];
    });
}

/** Each streaming service list as a tile (`service.<id>.<part>`). */
export function serviceTiles(shelves: ShelfConfig[]): CatalogTile[] {
  const config = shelves.find((c) => c.id === 'streaming-services');
  return resolveServices(config?.streamingServices).flatMap((service) =>
    service.lists.map((l, part) => ({
      key: `service.${service.id}.${part}`,
      title: service.name,
      kicker: l.title ?? undefined,
      logoUrl: service.logoUrl,
    })),
  );
}

export type SimilarSeed = Extract<BrowseList, { kind: 'similar' }> & { backdrop?: string };

/** "Because you watched": up to three movies and three shows, alternating (ListsCatalog.seeds). */
export function becauseYouWatched(items: ContinueWatchingItem[]): SimilarSeed[] {
  const seen = new Set<string>();
  const candidates: SimilarSeed[] = items.flatMap((i) => {
    const tmdb = i.externalIds?.tmdb?.trim();
    if (!tmdb || seen.has(tmdb)) return [];
    seen.add(tmdb);
    return [
      {
        kind: 'similar' as const,
        mediaType: isMovieId(i.seriesId) ? ('movie' as const) : ('series' as const),
        tmdbId: tmdb,
        seed: i.seriesTitle,
        backdrop: i.backdropUrl,
      },
    ];
  });
  const movies = candidates.filter((c) => c.mediaType === 'movie').slice(0, 3);
  const shows = candidates.filter((c) => c.mediaType === 'series').slice(0, 3);
  const out: SimilarSeed[] = [];
  for (let i = 0; i < Math.max(movies.length, shows.length); i++) {
    if (movies[i]) out.push(movies[i]!);
    if (shows[i]) out.push(shows[i]!);
  }
  return out;
}
