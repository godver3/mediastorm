/**
 * Startup timing instrumentation (dev mode only).
 * Records timestamps at key milestones and logs a summary
 * when the app becomes interactive.
 *
 * Usage:
 *   import { startupTiming } from '../services/startup-timing';
 *   startupTiming.mark('fonts_loaded');
 *   // ... later ...
 *   startupTiming.end(); // logs summary table
 */

const marks: { label: string; time: number }[] = [];
const t0 = globalThis.performance?.now?.() ?? Date.now();

function mark(label: string) {
  if (!__DEV__) return;
  const now = globalThis.performance?.now?.() ?? Date.now();
  marks.push({ label, time: now });
  const delta = Math.round(now - t0);
  console.log(`[Startup] ${label} @ +${delta}ms`);
}

function end() {
  if (!__DEV__) return;
  const now = globalThis.performance?.now?.() ?? Date.now();
  const total = Math.round(now - t0);
  console.log(`\n[Startup] ===== TIMING SUMMARY =====`);
  console.log(`[Startup] JS bundle start → now: ${total}ms`);
  let prev = t0;
  for (const m of marks) {
    const fromStart = Math.round(m.time - t0);
    const fromPrev = Math.round(m.time - prev);
    console.log(`[Startup]   ${m.label}: +${fromStart}ms (delta: ${fromPrev}ms)`);
    prev = m.time;
  }
  console.log(`[Startup] ================================\n`);
}

// Record the first mark automatically when this module is imported
mark('js_module_init');

export const startupTiming = { mark, end };
