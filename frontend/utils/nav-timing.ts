// Shared navigation timing for debugging slow page transitions.
// Set navStartTimestamp from the source page before router.push(),
// then read it from the destination page to measure end-to-end latency.

export let navStartTimestamp = 0;

export function markNavStart(): number {
  navStartTimestamp = Date.now();
  return navStartTimestamp;
}

export function msSinceNavStart(): number {
  if (!navStartTimestamp) return -1;
  return Date.now() - navStartTimestamp;
}
