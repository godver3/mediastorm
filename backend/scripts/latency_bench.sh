#!/usr/bin/env bash
# ============================================================================
# latency_bench.sh — repeatable cold-cache latency benchmark (OPP measurement).
#
# Drives the REAL prequeue + HLS playback HTTP flow (no browser) a set number
# of times against a running backend. Before each iteration it forces a cold
# state via the admin flush (scope = resolve by default, matching OPP-1/3/12);
# the same title is then prequeued, played to its first HLS segment, and the
# server-side click→first-frame sample is written to a local CSV together with
# the exact release that was selected. Later OPP runs append to their own CSV so
# you can diff improvements per media/release over the span of the work.
#
# Requirements
#   * backend running (dev-server.sh) and reachable at $BASE_URL
#   * TOKEN  — an account session token (Bearer) for the user APIs. A master
#     session works (canAccessUser allows any userId). This same token drives
#     the HLS playlist/segment fetches (they are on the protected router).
#   * ADMIN_COOKIE — the strmr_admin_session cookie for /admin/api endpoints.
#   * USER_ID — a profile UUID that exists in the DB.
#   * a title that produces an HLS session (HDR/DV/TrueHD/DTS): only then is a
#     complete t0→t4 sample emitted. SDR/native titles yield the prequeue phase
#     only (complete=false, noted in the CSV).
#
# Usage
#   TITLE_ID=tmdb:movie:152601 TITLE_NAME=Her USER_ID=28b5d729-... \
#   TOKEN='...' ADMIN_COOKIE='mediastorm_admin_session=...' \
#   ./scripts/latency_bench.sh -n 10 -s resolve
#
# Flags / env
#   -n N          iterations (default 10)
#   -s SCOPE      flush scope: all | resolve | stream (default resolve)
#   -t SECONDS    per-iteration prequeue timeout (default 360)
#   -o FILE       results CSV override
#   -f RELEASE    only keep/summarize samples whose releaseName contains this
#   BASE_URL      default http://localhost:7777
# ----------------------------------------------------------------------------
# Output: backend/cache/latency-bench/<titleId>-<scope>-<date>.csv (gitignored)
# plus a mean/p50/p95 summary + per-release breakdown on stdout.
# ============================================================================
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:7777}"
TOKEN="${TOKEN:-}"
ADMIN_COOKIE="${ADMIN_COOKIE:-}"
USER_ID="${USER_ID:-}"
TITLE_ID="${TITLE_ID:-}"
TITLE_NAME="${TITLE_NAME:-}"
MEDIA_TYPE="${MEDIA_TYPE:-movie}"
YEAR="${YEAR:-}"
IMDB_ID="${IMDB_ID:-}"
ITERATIONS=10
SCOPE=resolve
TIMEOUT=360
OUT="${OUT:-}"
RELEASE_FILTER="${RELEASE_FILTER:-}"

while getopts "n:s:t:o:f:h" opt; do
  case "$opt" in
    n) ITERATIONS="$OPTARG" ;;
    s) SCOPE="$OPTARG" ;;
    t) TIMEOUT="$OPTARG" ;;
    o) OUT="$OPTARG" ;;
    f) RELEASE_FILTER="$OPTARG" ;;
    h) sed -n '2,40p' "$0" | grep -v '^#!/' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag -$opt" >&2; exit 2 ;;
  esac
done
case "$SCOPE" in all|resolve|stream) ;; *) echo "bad scope: $SCOPE (all|resolve|stream)" >&2; exit 2 ;; esac

fail() { echo "!! $*" >&2; exit 1; }
for v in TOKEN ADMIN_COOKIE USER_ID TITLE_ID TITLE_NAME; do
  [ -n "${!v}" ] || fail "$v is required (see header; mask secrets in your shell)"
done

json_quote() { python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$1"; }

if [ -z "$OUT" ]; then
  # Anchor under backend/cache (gitignored) regardless of the working directory.
  OUT_DIR="$(cd "$(dirname "$0")" && pwd)/../cache/latency-bench"
  mkdir -p "$OUT_DIR"
  OUT="$OUT_DIR/${TITLE_ID//[:\/]/_}-${SCOPE}-$(date +%Y%m%d-%H%M%S).csv"
else
  mkdir -p "$(dirname "$OUT")"
fi

CSV_HEADER="ts,iteration,titleName,titleId,releaseName,serviceType,serviceProvider,complete,totalMs,prequeueMs,hlsCreateMs,ffmpegWarmupMs,serveWaitMs,candidates,notes"
[ -s "$OUT" ] || echo "$CSV_HEADER" >> "$OUT"

admin_curl() { curl -sS -H "Cookie: $ADMIN_COOKIE" "$@"; }
api_curl()   { curl -sS -H "Authorization: Bearer $TOKEN" "$@"; }

# Cold flush with HTTP-status verification: curl exits 0 even on a 401, so a
# bad/empty ADMIN_COOKIE would otherwise silently skip the flush and skew the
# run as "cold". Fail loudly and diagnose instead.
flush_cold() {
  local tmp code probe
  tmp="$(mktemp)"
  code="$(curl -sS -o "$tmp" -w '%{http_code}' -H "Cookie: $ADMIN_COOKIE" -X POST "$BASE_URL/admin/api/latency/flush?scope=$SCOPE" || true)"
  rm -f "$tmp"
  case "$code" in
    2[0-9][0-9]) return 0 ;;
  esac
  probe="$(curl -sS -o /dev/null -w '%{http_code}' -H "Cookie: $ADMIN_COOKIE" "$BASE_URL/admin/api/latency?limit=1" || true)"
  local val="${ADMIN_COOKIE#mediastorm_admin_session=}"
  echo "   flush failed (HTTP $code); GET /admin/api/latency -> HTTP $probe; cookie value length=${#val}" >&2
  if [ -z "$val" ]; then
    echo "   -> ADMIN_COOKIE is empty. Copy a fresh ⚡ command from /admin/latency (logged-in browser) so it carries the live session token." >&2
  elif [ "$code" = "403" ] || [ "$probe" = "403" ]; then
    echo "   -> session token invalid/expired or not a master account. Re-login to /admin and re-copy the command." >&2
  fi
  return 1
}

# POST the real prequeue request; echoes the prequeueId ("" on failure).
start_prequeue() {
  local body="{\"titleId\":$(json_quote "$TITLE_ID"),\"titleName\":$(json_quote "$TITLE_NAME"),"
  body+="\"mediaType\":$(json_quote "$MEDIA_TYPE"),\"userId\":$(json_quote "$USER_ID")"
  [ -n "$YEAR" ]    && body+=",\"year\":$YEAR"
  [ -n "$IMDB_ID" ] && body+=",\"imdbId\":$(json_quote "$IMDB_ID")"
  body+="}"
  api_curl -X POST -H 'Content-Type: application/json' -d "$body" "$BASE_URL/api/playback/prequeue" \
    | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("prequeueId",""))
except Exception: print("")' | tr -d '\n' || echo ""
}

# Poll the prequeue status; prints the raw JSON once ready/failed/expired.
wait_prequeue() {
  local id="$1" elapsed=0
  while [ "$elapsed" -lt "$TIMEOUT" ]; do
    local body
    body="$(api_curl "$BASE_URL/api/playback/prequeue/$id" || true)"
    if echo "$body" | grep -q '"status"'; then
      case "$(echo "$body" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status",""))' 2>/dev/null || echo '')" in
        ready|failed|expired) echo "$body"; return 0 ;;
      esac
    fi
    sleep 3; elapsed=$((elapsed+3))
  done
  return 1
}

# Drive the HLS session to its first media segment, which is what lands t2→t4
# and produces the complete sample. Uses the same Bearer token as the prequeue.
drive_hls() {
  local status_json="$1"
  local playlist
  playlist="$(echo "$status_json" | python3 -c '
import json,sys
try: print(json.load(sys.stdin).get("hlsPlaylistUrl","") or "")
except Exception: print("")')"
  [ -n "$playlist" ] || return 1

  BASE_URL="$BASE_URL" TOKEN="$TOKEN" python3 - "$playlist" <<'PY'
import os, re, sys, time, urllib.request, urllib.error

base = os.environ["BASE_URL"]
token = os.environ.get("TOKEN", "")
pl = sys.argv[1]

def resolve(url):
    if url.startswith("http"): return url
    if url.startswith("/api/"): return base + url
    if url.startswith("/"): return base + "/api" + url
    return base + "/api/" + url

def fetch(url, retries=1, pause=2, timeout=60):
    req = urllib.request.Request(url, headers={"Authorization": "Bearer " + token} if token else {})
    last = None
    for i in range(retries + 1):
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return r.status, r.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            last = e
            time.sleep(pause)
        except Exception as e:
            last = e
            time.sleep(pause)
    return -1, str(last)

url = resolve(pl)
code, text = fetch(url)
if code != 200:
    sys.exit(1)  # playlist unreachable

# Master playlist? switch to the first media playlist URI.
if "#EXT-X-STREAM-INF" in text:
    for line in text.splitlines():
        line = line.strip()
        if line and not line.startswith("#") and ".m3u8" in line:
            url = resolve(line) if not re.match(r"^https?://", line) else line
            break
    code, text = fetch(url)
    if code != 200:
        sys.exit(2)

# Collect media segment URIs (segmentN.ts / .m4s / .webm), skipping playlists/init.
segments = []
for line in text.splitlines():
    line = line.strip()
    if not line or line.startswith("#"):
        continue
    if not re.search(r"segment[0-9]+\.(ts|m4s|webm)", line, re.I):
        continue
    segments.append(urllib.parse.urljoin(url, line))

if not segments:
    sys.exit(3)

# First numeric segment is what the tracker counts as the first frame (t3/t4).
# Transcode warmup can take tens of seconds: retry until the server delivers it.
for seg in segments[:2]:
    for attempt in range(45):
        status, _ = fetch(seg, retries=0, pause=0, timeout=120)
        if status == 200:
            sys.exit(0)
        time.sleep(2)
sys.exit(4)
PY
}

# Append the sample matched by prequeueId to the CSV. When the server has no
# sample for this prequeue (non-HLS stream, or an HLS session that never served
# a segment) we still record the client-measured prequeue phase so the iteration
# isn't lost — and deliberately never attribute another iteration's sample here.
# Degrades gracefully on empty/non-JSON admin responses.
record_sample() {
  local prequeue_id="$1" iteration="$2" client_prequeue_ms="$3"
  local tsv tmp code
  tsv="$(date -u +%FT%TZ),$iteration"
  tmp="$(mktemp)"
  code="$(curl -sS -o "$tmp" -w '%{http_code}' -H "Cookie: $ADMIN_COOKIE" "$BASE_URL/admin/api/latency?limit=50" || true)"
  if ! case "$code" in 2[0-9][0-9]) true ;; *) false ;; esac; then
    echo "   warn: admin latency API HTTP $code — sample skipped (is \$ADMIN_COOKIE valid, backend restarted?)" >&2
    rm -f "$tmp"
    return 0
  fi
  TITLE_ID="$TITLE_ID" TITLE_NAME="$TITLE_NAME" \
  python3 - "$prequeue_id" "$OUT" "$tsv" "$tmp" "$client_prequeue_ms" <<'PY' || true
import csv, json, os, sys
try:
    d = json.load(open(sys.argv[4]))
except Exception as e:
    print("   warn: admin latency API returned invalid JSON (skipped):", e, file=sys.stderr)
    sys.exit(0)
samples = d.get('samples', []) or []
target = sys.argv[1]
pick = None
for s in samples:                       # Latest() is newest-first
    if s.get('prequeueId') == target:
        pick = s; break

def cand_summary(cands):
    if not cands:
        return ""
    parts = []
    for c in cands:
        parts.append("%d:%s:%s:%d" % (c.get('index', 0) or 0, c.get('releaseName','') or '',
                                       c.get('outcome','') or '', c.get('durationMs', -1) or -1))
    return ';'.join(parts)

def write_row(row):
    with open(sys.argv[2], 'a', newline='') as f:
        csv.writer(f).writerow(row)

if pick is None:
    # No server HLS sample for this prequeue (non-HLS stream / no segment
    # served). Prequeue is the phase every OPP cares about, so record the
    # client-measured t0->t1 instead of dropping the iteration.
    write_row(sys.argv[3].split(',', 1) + [
        os.environ.get('TITLE_NAME','') or '', os.environ.get('TITLE_ID','') or '',
        '', '', '', 'no', -1, int(sys.argv[5] or 0), -1, -1, -1, '',
        'no server HLS sample; prequeueMs client-measured (non-HLS stream)'])
    print("   (no server sample — recorded client-measured prequeueMs=%sms)" % sys.argv[5], file=sys.stderr)
    sys.exit(0)

def field(k):
    v = pick.get(k)
    return v if v is not None else -1
write_row(sys.argv[3].split(',', 1) + [pick.get('titleName','') or '', pick.get('titleId','') or '',
      pick.get('releaseName','') or '', pick.get('serviceType','') or '',
      pick.get('serviceProvider','') or '', 'yes' if pick.get('complete') else 'no',
      field('totalMs'), field('prequeueMs'), field('hlsCreateMs'),
      field('ffmpegWarmupMs'), field('serveWaitMs'), cand_summary(pick.get('candidates') or []),
      ';'.join(pick.get('notes',[]) or [])])
PY
  rm -f "$tmp"
}

echo "== latency bench: $TITLE_NAME ($TITLE_ID) on $BASE_URL =="
echo "   iterations=$ITERATIONS flush=$SCOPE out=$OUT"
admin_curl -X POST "$BASE_URL/admin/api/latency/clear" >/dev/null 2>&1 || true   # clear sample window once

# Monotonic millisecond timestamp (host may lack %N; python3 is available).
date_ms() { python3 -c 'import time; print(int(time.time() * 1000))'; }

pass=0; fail=0
for i in $(seq 1 "$ITERATIONS"); do
  echo ""
  echo "== iteration $i/$ITERATIONS =="
  if ! flush_cold; then fail=$((fail+1)); continue; fi

  t0ms="$(date_ms)"
  id="$(start_prequeue)"
  if [ -z "$id" ]; then echo "   prequeue start failed"; fail=$((fail+1)); continue; fi
  echo "   prequeueId=$id"

  if ! status_json="$(wait_prequeue "$id" )"; then
    echo "   timed out waiting for $id after ${TIMEOUT}s"; fail=$((fail+1)); continue
  fi
  t1ms="$(date_ms)"
  client_prequeue_ms=$((t1ms - t0ms))
  echo "   status: $(echo "$status_json" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d.get("status"),"stage="+str(d.get("progressStage","")))' 2>/dev/null || echo '?') (client t0->t1=${client_prequeue_ms}ms)"

  sleep 2                                  # let the ready/pending timestamps settle
  if drive_hls "$status_json"; then
    echo "   first segment served (complete sample recorded)"
  else
    echo "   no media segments served — non-HLS stream records the prequeue phase only (complete=false)"
  fi
  sleep 1
  record_sample "$id" "$i" "$client_prequeue_ms"
  echo "   row: $(tail -1 "$OUT")"
  pass=$((pass+1))
done

echo ""
echo "== done: $pass iterations, $fail failures; results in $OUT =="
[ -n "$RELEASE_FILTER" ] && echo "   (summary filtered to releases containing: $RELEASE_FILTER)"
RELEASE_FILTER="$RELEASE_FILTER" python3 - "$OUT" <<'PY'
import csv, os, statistics, sys
path = sys.argv[1]
rel = os.environ.get("RELEASE_FILTER", "")
rows = []
with open(path) as f:
    for row in csv.DictReader(f):
        if rel and rel not in (row.get("releaseName") or ""):
            continue
        try:
            t = int(row["totalMs"]); p = int(row["prequeueMs"])
        except Exception:
            continue
        rows.append((row, t, p))
if not rows:
    print("   (no parseable rows for summary)")
    raise SystemExit(0)

def stat(v):
    if not v:
        return "(none)"
    out = f"mean={statistics.mean(v):.0f}ms  p50={statistics.median(v)}ms  max={max(v)}ms"
    if len(v) >= 5:
        out += f"  p95={statistics.quantiles(v, n=20)[18]}ms"
    return out

complete = [(r, t, p) for r, t, p in rows if t >= 0]
ps = [p for _, _, p in rows if p >= 0]
print(f"   iterations={len(rows)}  complete(samples)={len(complete)}  prequeue-only={len(rows)-len(complete)}")
print(f"   total    {stat([t for _, t, _ in complete])}   (complete samples only)")
print(f"   prequeue {stat(ps)}   (all rows; client-measured for non-HLS)")
# OPP-3: per-candidate outcomes across the run — probe_rejected/"articles_unavailable"
# durations are the dead-release rejection latency, diffable before/after.
outcomes = {}
for row, _, _ in rows:
    for item in (row.get("candidates") or "").split(";"):
        if not item:
            continue
        parts = item.split(":")
        if len(parts) < 4:
            continue
        outcome = parts[2]
        try:
            ms = int(parts[3])
        except Exception:
            ms = -1
        bucket = outcomes.setdefault(outcome, [])
        if ms >= 0:
            bucket.append(ms)
if outcomes:
    pieces = []
    for outcome, ms in sorted(outcomes.items()):
        label = f"{outcome} x{len(ms)}"
        if ms:
            label += f" (mean={statistics.mean(ms):.0f}ms)"
        pieces.append(label)
    print("   candidates: " + ", ".join(pieces))
print("   releases:")
by_release = {}
for row, t, p in rows:
    key = (row.get("releaseName") or "(unknown)") + "|" + (row.get("serviceProvider") or "")
    bucket = by_release.setdefault(key, {"complete": [], "prequeue": []})
    (bucket["complete"] if t >= 0 else bucket["prequeue"]).append(t if t >= 0 else p)
for key, bucket in by_release.items():
    label = key
    if bucket["complete"]:
        label += f"  (complete total mean={statistics.mean(bucket['complete']):.0f}ms)"
    if bucket["prequeue"]:
        label += f"  (prequeue-only mean={statistics.mean(bucket['prequeue']):.0f}ms)"
    print(f"     x{len(bucket['complete'])+len(bucket['prequeue'])}  {label}")
PY
