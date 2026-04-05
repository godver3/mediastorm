# Claude Code Memory

## Repository Structure

This project is split across three repositories:

| Repo | GitHub | Local Path | Visibility |
|------|--------|------------|------------|
| **Backend** | `godver3/mediastorm` | `~/strmr/` (repo root) | Public |
| **Frontend** | `godver3/mediastorm-frontend` | `~/strmr/frontend/` (separate git repo) | Private |
| **KSPlayer fork** | `godver3/KSPlayer` branch `strmr-fixes` | `~/ksplayer/` (symlinked into repo root, gitignored) | Private |

- The backend repo (`mediastorm`) is the main public repo. `frontend/` is gitignored here.
- The frontend is its own git repo inside the `frontend/` directory, tracking `mediastorm-frontend`.
- KSPlayer is a separate clone symlinked in for iOS native player builds.

**Working with the frontend repo:**
```bash
cd ~/strmr/frontend
git status          # frontend repo context
git push origin main
```

**Working with the backend repo:**
```bash
cd ~/strmr
git status          # backend/public repo context
git push mediastorm master
```

**IMPORTANT — Two separate repos means separate git operations:**
When running `git diff`, `git status`, `git log`, or any git command, always handle the backend (`~/strmr/`) and frontend (`~/strmr/frontend/`) repos **separately**. A diff in the root repo will NOT show frontend changes, and vice versa. Always check both repos independently when reviewing changes that span backend and frontend.

## Project TODO
Reference `docs/TODO.md` for bugs, planned features, and improvements. Check this file when:
- Starting new work to see existing priorities
- Adding new bugs or feature requests
- Looking for context on known issues

**When updating the TODO list:**
- When working on an item, mark it **In Progress Testing** with a brief note of the tentative solution
- Only remove items once the user has confirmed the fix works (do NOT remove on your own)
- Only keep open/reported bugs and pending features
- **IMPORTANT:** This file is served publicly via Docker (port 8082). Never include confidential information, API keys, credentials, internal URLs, security vulnerabilities, or any sensitive data.

**Note:** `docs/TODO.md` is gitignored - do not attempt to commit changes to it.

## Docker Hub
- Repository name: `godver3/mediastorm`
- Always use `mediastorm` as the image name when building and pushing

## Build Commands

### Backend Docker Build
Build from repo root (not from backend directory):
```bash
cd /root/mediastorm
docker build -t godver3/mediastorm:latest -f backend/Dockerfile .
docker push godver3/mediastorm:latest
```

Note: The Dockerfile expects to be run from the repo root with `-f backend/Dockerfile` since it references paths like `backend/go.mod` and `backend/parse_title.py`.

### Local Go Builds
When building the Go backend locally (e.g., `go build` in `/root/mediastorm/backend`), a binary named `mediastorm` is created. This binary is not needed and should be deleted - it's a development artifact. The production backend runs in Docker.

## Backend Testing

**IMPORTANT:** All backend code changes should include associated tests.

**Guidelines:**
- When adding new handlers, add corresponding tests in `handlers/*_test.go`
- When modifying services, add or update tests in `services/*/service_test.go`
- When adding internal packages, add tests in `internal/*_test.go`
- Run tests before committing: `cd backend && go test ./...`
- Run specific package tests: `go test ./handlers/... -v`

**Test file naming:**
- Tests go in the same directory as the code they test
- Use `_test.go` suffix (e.g., `admin_ui_test.go` for `admin_ui.go`)
- Use `package handlers_test` for black-box testing or `package handlers` for white-box testing

**Existing test coverage:** See `docs/TODO.md` Testing section for current coverage status.

## KSPlayer Fork

The iOS/tvOS native player uses a forked KSPlayer: `godver3/KSPlayer` branch `strmr-fixes`.

**Local clone:** `~/ksplayer` (symlinked into repo root as `ksplayer/`, gitignored)

**Key files with our fixes:**
- `Sources/KSPlayer/MEPlayer/Resample.swift` — DV Profile 5 color fix (forces BT.2020/PQ when isDovi but metadata is UNSPECIFIED)
- `Sources/KSPlayer/Subtitle/KSParseProtocol.swift` — italic obliqueness fix (0.15 instead of 1.0)

**Workflow for KSPlayer changes:**
1. Edit files in `~/ksplayer/`
2. Commit and push to `strmr-fixes` branch
3. Run `cd frontend && npx expo prebuild --platform ios --clean` to pull updated pods
4. Build with `npm run ios -- --device`

**Podfile source:** set in `frontend/plugins/with-ksplayer.js` (Expo config plugin)

**Upstream:** `kingslay/KSPlayer` main branch. To sync upstream changes:
```bash
cd ~/ksplayer
git fetch upstream
git rebase upstream/main
# Resolve any conflicts with our fixes, then force push
git push origin strmr-fixes --force
```

## Development Scripts

### dev.sh - Start/Stop/Restart Services
Located at repo root. Use this to manage backend and frontend during development:

```bash
# Start/stop/restart both services
./dev.sh start
./dev.sh stop
./dev.sh restart

# Target specific service
./dev.sh start backend
./dev.sh stop backend
./dev.sh restart backend

./dev.sh start frontend
./dev.sh stop frontend
./dev.sh restart frontend
```

**Ports:**
- Backend: 7777
- Frontend: 8081

**Logs (IMPORTANT - check these for debugging):**
- Backend: `.logs/backend.log`
- Frontend: `.logs/frontend.log`

When debugging issues or errors, ALWAYS check these log files first:
```bash
tail -f .logs/backend.log   # Follow backend logs
tail -f .logs/frontend.log  # Follow frontend logs
```

**Important:**
- After making backend code changes (Go), run `./dev.sh restart backend` to apply them. **Run this command in the background** to avoid blocking.
- Frontend JS/TS changes have hot reload - NO restart needed, changes apply automatically.
- dev.sh commands are non-blocking - no need to wait for output, they complete immediately.

### monitor.sh - System Monitoring for Debugging

Located at repo root. Use this to diagnose system instability or performance issues:

```bash
# Run in background (recommended)
nohup ./monitor.sh 30 > /dev/null 2>&1 &

# Run in foreground with live status
./monitor.sh [interval_seconds] [output_dir]

# Default: 30 second intervals, output to .monitoring/
```

**Output Files (in `.monitoring/`):**
| File | Contents |
|------|----------|
| `metrics.log` | System metrics (memory, disk, IO, network connections) |
| `processes.log` | Process stats (CPU, memory, FDs, threads for Go/Python) |
| `goroutines.log` | Go runtime stats, heap profiles, goroutine dumps (every 5 min) |
| `alerts.log` | Threshold alerts (high CPU/memory/FDs/goroutines) |
| `goroutine_dump_*.txt` | Full goroutine stack traces (kept last 20) |

**What it tracks:**
- System memory, disk, IO, network connections on port 7777
- Go backend: CPU, memory, open file descriptors, thread count
- Python subprocesses: CPU, memory, FDs, threads
- Zombie/defunct processes
- Go runtime: goroutine count, heap stats, GC info
- Full goroutine stack dumps for deadlock detection

**Alert thresholds (logged to alerts.log):**
- CPU > 90%
- Memory > 85%
- Open FDs > 10,000
- Goroutines > 1,000

**Debug Endpoints (localhost-only, no auth required):**
```bash
# JSON runtime stats (goroutines, heap, GC)
curl http://localhost:7777/api/debug/runtime

# Full goroutine stack traces
curl "http://localhost:7777/api/debug/pprof/goroutine?debug=2"

# Heap profile
curl "http://localhost:7777/api/debug/pprof/heap?debug=1"

# All pprof endpoints available at /api/debug/pprof/
```

**Quick debugging workflow:**
```bash
# Start monitoring
nohup ./monitor.sh 30 > /dev/null 2>&1 &

# Watch for alerts
tail -f .monitoring/alerts.log

# Check recent goroutine dumps if issues occur
ls -lt .monitoring/goroutine_dump_*.txt | head -5
```

## Settings & API Keys

Application settings (including TMDB/TVDB API keys, metadata config, etc.) are stored in `backend/cache/settings.json`. When you need to read API keys or other settings for debugging/testing, read them from this file:
```bash
cat backend/cache/settings.json | python3 -c "import sys,json; d=json.load(sys.stdin); print(json.dumps(d['metadata'], indent=2))"
```

## Authenticated API Testing

Most backend API endpoints require auth via `Authorization: Bearer <token>`, `X-PIN`, or `?token=`.

For local API testing, retrieve a live session token from the Docker Postgres container:
```bash
docker exec mediastorm-postgres psql -U mediastorm -d mediastorm -Atc "SELECT token FROM sessions WHERE expires_at > now() ORDER BY created_at DESC LIMIT 1;"
```

Then use it with curl:
```bash
curl -sS -H 'Authorization: Bearer <token>' http://127.0.0.1:7777/api/auth/me
```

Wrap the `Authorization` header value in single quotes so shell-special characters in the token are preserved.

**Note:** The `/api/settings` endpoint masks sensitive values — always use the file directly.

## Database

**PostgreSQL** is the sole data store. All user data, sessions, settings, watchlists, queues, etc. live in Postgres.

- **Driver:** `jackc/pgx/v5` with connection pooling (`pgxpool`)
- **Migrations:** Goose v3, embedded SQL files in `backend/internal/datastore/migrations/`
- **Connection:** Set via `DATABASE_URL` env var (e.g., `postgres://user:pass@localhost:5432/mediastorm?sslmode=disable`)
- **Repository pattern:** `backend/internal/datastore/pg_*.go` — one file per table, all implement interfaces in `repositories.go`
- **Schema:** `backend/internal/datastore/migrations/001_initial_schema.sql` (19 tables)
- **JSON migration:** On first startup with Postgres, `migrate_json.go` auto-migrates legacy `cache/*.json` files (renamed to `.migrated` after)

**Key tables:** `accounts`, `users` (profiles), `sessions`, `watchlist`, `watch_history`, `playback_progress`, `user_settings`, `client_settings`, `custom_lists`, `prequeue`, `prewarm`, `import_queue`, `file_health`, `media_files`, `invitations`, `clients`

## API Authentication

**IMPORTANT:** Most API endpoints require authentication. When testing endpoints with curl, you must include a valid session token.

**Token can be passed via (checked in this order):**
1. Header: `Authorization: Bearer <token>`
2. Header: `X-PIN: <token>` (for reverse proxy compatibility)
3. Query param: `?token=<token>` (for streaming endpoints)

**Obtaining a token for testing:**
```bash
# Login to get a session token
curl -s -X POST http://localhost:7777/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"YOUR_USER","password":"YOUR_PASS"}' | jq .token

# Or query the sessions table directly (psql is inside the Docker container)
docker exec mediastorm-postgres psql -U mediastorm -d mediastorm \
  -c "SELECT token FROM sessions WHERE expires_at > now() ORDER BY created_at DESC LIMIT 1;"
```

**IMPORTANT:** When using tokens with special characters (`=`, `+`, `/`) in curl, use single quotes around the header value to prevent shell expansion:
```bash
curl -s -H 'Authorization: Bearer TOKEN_HERE' http://localhost:7777/api/auth/me
```

**Session details:**
- Sessions are stored in the PostgreSQL `sessions` table (also cached in-memory for fast validation)
- Regular sessions expire after 30 days; `rememberMe` sessions last ~100 years
- Expired sessions cleaned up hourly by background job
- Tokens are 32 cryptographically random bytes, base64url-encoded

**Account model:**
- **Master account:** `id="master"`, `username="admin"`, `is_master=true` — auto-created on first startup with default password `admin`
- **Regular accounts:** created via master, UUID-based IDs, optional expiration
- Each account can have multiple user profiles (the `users` table)

**Public endpoints (no auth required):**
- `/health` - Health check
- `/api/version` - Version info
- `/api/auth/default-password` - Check if default password still set
- `/api/auth/login` - Login endpoint
- `/api/users/{userID}/icon` - Profile icons
- `/api/static/*` - Static assets
- `/api/debug/*` - Debug endpoints (localhost only)
- `/api/homepage` - Homepage integration (API key protected)

**Example authenticated requests:**
```bash
# With Authorization header
curl -H "Authorization: Bearer YOUR_TOKEN" http://localhost:7777/api/settings

# With query parameter (for streaming)
curl "http://localhost:7777/api/video/stream?token=YOUR_TOKEN&url=..."

# Check current session info
curl -H "Authorization: Bearer YOUR_TOKEN" http://localhost:7777/api/auth/me
```

**Permission levels:**
- **Regular auth** (`AccountAuthMiddleware`): Most endpoints (`/api/settings` GET, `/api/search`, `/api/discover/*`, etc.)
- **Master only** (`MasterOnlyMiddleware`): Admin endpoints (`/api/settings` PUT, `/api/accounts/*`, `/api/admin/*`, profile reassignment)
- **Profile-scoped** (`ProfileOwnershipMiddleware`): User endpoints (`/api/users/{userID}/*` — regular accounts can only access their own profiles; master can access all)
