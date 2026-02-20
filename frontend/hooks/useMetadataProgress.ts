import { useEffect, useRef, useState } from 'react';

import { apiService, type MetadataProgressSnapshot } from '@/services/api';

const POLL_INTERVAL_MS = 2000;
// After enabled goes false, keep polling for this many cycles to catch
// tasks that start after the startup bundle returns.
const GRACE_POLLS = 5;

/**
 * Polls GET /api/metadata/progress on mount.
 * `enabled` (typically `!ready` from startup) controls when to stop:
 * polling continues even after `enabled` goes false as long as the backend
 * has active enrichment tasks (activeCount > 0) OR we're still within a
 * grace period. This lets the UI show progress that outlasts the startup fetch.
 * Errors are silently swallowed — progress is supplementary UX.
 */
export function useMetadataProgress(enabled: boolean, initialDelayMs = 0): MetadataProgressSnapshot | null {
  const [snapshot, setSnapshot] = useState<MetadataProgressSnapshot | null>(null);
  const enabledRef = useRef(enabled);
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  // Track consecutive polls with activeCount=0 after enabled goes false
  const idlePollsRef = useRef(0);
  // Once we've seen active tasks, don't apply the grace period — just wait for completion
  const sawActiveRef = useRef(false);
  enabledRef.current = enabled;

  useEffect(() => {
    let cancelled = false;

    const stopTimer = () => {
      if (timerRef.current) {
        clearInterval(timerRef.current);
        timerRef.current = null;
      }
    };

    const poll = async () => {
      if (cancelled) return;
      try {
        const data = await apiService.getMetadataProgress();
        if (cancelled) return;

        if (data.activeCount > 0) {
          sawActiveRef.current = true;
          idlePollsRef.current = 0;
          setSnapshot(data);
        } else if (!enabledRef.current) {
          // No active tasks and startup is done
          if (sawActiveRef.current) {
            // We saw activity and it's now complete — done
            console.log('[MetadataProgress] enrichment complete, stopping');
            setSnapshot(null);
            stopTimer();
          } else {
            // Never saw activity — use grace period in case tasks haven't started yet
            idlePollsRef.current++;
            if (idlePollsRef.current >= GRACE_POLLS) {
              console.log('[MetadataProgress] grace period expired with no activity, stopping');
              setSnapshot(null);
              stopTimer();
            }
          }
        } else {
          // No active tasks but startup still loading — keep polling
          setSnapshot(null);
        }
      } catch {
        // Silently ignore — progress is supplementary
      }
    };

    const initialTimer = setTimeout(() => {
      void poll();
      timerRef.current = setInterval(poll, POLL_INTERVAL_MS);
    }, initialDelayMs);

    return () => {
      cancelled = true;
      clearTimeout(initialTimer);
      stopTimer();
    };
  }, []); // Mount-only — self-terminates via enabledRef + activeCount

  return snapshot;
}
