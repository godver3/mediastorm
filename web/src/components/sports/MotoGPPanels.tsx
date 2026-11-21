import { useQuery } from '@tanstack/react-query';
import {
  circuitMapUrl,
  getMotoGPCircuit,
  getMotoGPStandings,
  standingsKeys,
} from '@/api/sportsStandings';
import { Skeleton } from '@/components/Spinner';

/**
 * MotoGP weekend extras the native race page shows (SportsRaceEnrichmentModels.swift): the riders'
 * championship and the circuit guide with its outline.
 */
export function MotoGPPanels({ providerEventId }: { providerEventId: string }) {
  const standings = useQuery({
    queryKey: standingsKeys.motogp(),
    queryFn: ({ signal }) => getMotoGPStandings(signal),
    staleTime: 30 * 60_000,
  });
  const circuit = useQuery({
    queryKey: standingsKeys.circuit(providerEventId),
    queryFn: ({ signal }) => getMotoGPCircuit(providerEventId, signal),
    staleTime: 60 * 60_000,
    enabled: !!providerEventId,
  });
  const c = circuit.data?.circuit;
  const map = circuitMapUrl(c);
  const facts: [string, string | undefined][] = c
    ? [
        ['Length', c.lengthMeters ? `${(c.lengthMeters / 1000).toFixed(2)} km` : undefined],
        ['Left corners', c.leftCorners?.toString()],
        ['Right corners', c.rightCorners?.toString()],
        ['Longest straight', c.longestStraightMeters ? `${c.longestStraightMeters} m` : undefined],
      ]
    : [];
  const riders = standings.data?.standings ?? [];

  return (
    <>
      {(circuit.isLoading || c) && (
        <section className="rounded-2xl border border-line bg-surface p-5">
          <h2 className="mb-3 text-base font-semibold">{c?.name || 'Circuit'}</h2>
          {circuit.isLoading ? (
            <Skeleton className="h-40 rounded-xl" />
          ) : (
            <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_200px] sm:items-center">
              {map ? (
                <img
                  src={map}
                  alt={`${c?.name ?? 'Circuit'} outline`}
                  className="max-h-56 w-full rounded-xl bg-white/5 object-contain p-3"
                />
              ) : (
                <div />
              )}
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
                {facts
                  .filter(([, v]) => v)
                  .map(([k, v]) => (
                    <div key={k} className="contents">
                      <dt className="text-muted">{k}</dt>
                      <dd className="text-right tabular-nums">{v}</dd>
                    </div>
                  ))}
              </dl>
            </div>
          )}
        </section>
      )}
      <section className="overflow-x-auto rounded-2xl border border-line bg-surface p-5">
        <h2 className="mb-3 text-base font-semibold">
          Riders' championship
          {standings.data?.season && (
            <span className="ml-2 text-sm font-normal text-faint">{standings.data.season}</span>
          )}
        </h2>
        {standings.isLoading ? (
          <Skeleton className="h-40 rounded-xl" />
        ) : riders.length === 0 ? (
          <p className="text-sm text-muted">
            {standings.data?.reason || 'The championship table is unavailable right now.'}
          </p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-xs text-faint">
                <th className="py-1 pr-2 font-semibold">Pos</th>
                <th className="py-1 pr-2 font-semibold">Rider</th>
                <th className="hidden py-1 pr-2 font-semibold sm:table-cell">Team</th>
                <th className="py-1 pr-2 text-right font-semibold">Wins</th>
                <th className="py-1 text-right font-semibold">Pts</th>
              </tr>
            </thead>
            <tbody>
              {riders.slice(0, 25).map((r) => (
                <tr key={r.riderId || r.name} className="border-t border-line">
                  <td className="py-2 pr-2 font-semibold tabular-nums">{r.position ?? '–'}</td>
                  <td className="py-2 pr-2">
                    {r.name}
                    {r.number != null && (
                      <span className="ml-2 text-xs text-faint">#{r.number}</span>
                    )}
                  </td>
                  <td className="hidden py-2 pr-2 text-muted sm:table-cell">
                    {r.team ?? r.constructor}
                  </td>
                  <td className="py-2 pr-2 text-right text-muted tabular-nums">{r.wins ?? '–'}</td>
                  <td className="py-2 text-right font-semibold tabular-nums">
                    {r.points != null ? r.points : '–'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </>
  );
}
