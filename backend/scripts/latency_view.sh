#!/usr/bin/env bash
# ============================================================================
# latency_view.sh — inspect the click→first-frame latency window of a running
# backend and (optionally) flush the playback caches to force a cold repeat.
#
# The measurement is passive: your real clicks in the app feed the numbers.
# Typical loop for a cold test of the SAME title N times:
#
#   ./scripts/latency_view.sh --flush     # make the next play cold
#   # ... play the title in the app ...
#   ./scripts/latency_view.sh             # read the samples
#
# Or watch continuously:
#   watch -n2 ./scripts/latency_view.sh
#
# Requirements: backend on localhost:8080 with a master session cookie for the
# admin endpoints. Pass BASE_URL / ADMIN_COOKIE to override.
# ============================================================================
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
COOKIE="${ADMIN_COOKIE:-}"   # e.g. "mediastorm_admin_session=abc..."

FLUSH=false
for arg in "$@"; do
  case "$arg" in
    --flush) FLUSH=true ;;
    --help|-h)
      sed -n '1,22p' "$0" | grep -v '^#!/' | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown arg: $arg (use --flush)" >&2; exit 2 ;;
  esac
done

curl_args=(-sS)
if [ -n "$COOKIE" ]; then
  curl_args+=(-H "Cookie: $COOKIE")
fi

if [ "$FLUSH" = true ]; then
  echo "== Flushing playback caches (cold test reset) =="
  SCOPE="${FLUSH_SCOPE:-all}"
  curl "${curl_args[@]}" -X POST "$BASE_URL/admin/api/latency/flush?scope=$SCOPE"
  echo
fi

echo "== Latency samples =="
curl "${curl_args[@]}" "$BASE_URL/admin/api/latency?limit=50" \
  | python3 -c '
import json,sys
d = json.load(sys.stdin)
s = d["stats"]
t = s["totalMs"]
print(f"samples={d[\"total\"]} complete={d[\"complete\"]} "
      f"total p50={t[\"p50Ms\"]}ms p95={t[\"p95Ms\"]}ms max={t[\"maxMs\"]}ms "
      f"prequeue p50={s[\"prequeueMs\"][\"p50Ms\"]}ms "
      f"ffmpegWarmup p50={s[\"ffmpegWarmupMs\"][\"p50Ms\"]}ms")
for x in reversed(d["samples"]):
    fields = [x.get("id",""), x.get("titleName","") or "–",
              x.get("serviceType","") or "–",
              x.get("totalMs","–"), x.get("prequeueMs","–"),
              x.get("hlsCreateMs","–"), x.get("ffmpegWarmupMs","–"),
              x.get("serveWaitMs","–"), "yes" if x.get("complete") else "no"]
    print("  " + " | ".join(str(f) for f in fields))
' || { echo "(failed to parse response — is the backend up?)" >&2; exit 1; }