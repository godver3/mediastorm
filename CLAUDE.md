# Claude Code Memory

## Project TODO
Reference `docs/TODO.md` for bugs, planned features, and improvements. Check this file when:
- Starting new work to see existing priorities
- Adding new bugs or feature requests
- Looking for context on known issues

**When updating the TODO list:**
- Delete completed items (status: Fixed) to keep the list clean
- Only keep open/reported bugs and pending features
- **IMPORTANT:** This file is served publicly via Docker (port 8082). Never include confidential information, API keys, credentials, internal URLs, security vulnerabilities, or any sensitive data.

**Note:** `docs/TODO.md` is gitignored - do not attempt to commit changes to it.

## Docker Hub
- Repository name: `godver3/strmr` (NOT `strmr-backend`)
- Always use `strmr` as the image name when building and pushing

## Build Commands

### Backend Docker Build
Build from repo root (not from backend directory):
```bash
cd /root/strmr
docker build -t godver3/strmr:latest -f backend/Dockerfile .
docker push godver3/strmr:latest
```

Note: The Dockerfile expects to be run from the repo root with `-f backend/Dockerfile` since it references paths like `backend/go.mod` and `backend/parse_title.py`.

### Local Go Builds
When building the Go backend locally (e.g., `go build` in `/root/strmr/backend`), a binary named `strmr` is created. This binary is not needed and should be deleted - it's a development artifact. The production backend runs in Docker.

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

## GitHub Actions

### Docker Build Workflow
Located at `.github/workflows/docker-build.yml`. Automatically builds and pushes to Docker Hub when:
- Changes are pushed to `master` branch in the `backend/` directory
- Manually triggered via workflow_dispatch

**Required secrets:**
- `DOCKERHUB_USERNAME` - Docker Hub username
- `DOCKERHUB_TOKEN` - Docker Hub access token

**Platforms built:**
- `linux/amd64`
- `linux/arm64`

**Tags pushed:**
- `godver3/strmr:latest`
- `godver3/strmr:<commit-sha>`

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

## API Authentication

**IMPORTANT:** Most API endpoints require authentication. When testing endpoints with curl, you must include a valid session token.

**Token can be passed via:**
- Header: `Authorization: Bearer <token>`
- Header: `X-PIN: <token>` (for reverse proxy compatibility)
- Query param: `?token=<token>` (for streaming endpoints)

**Sessions storage:**
- Sessions are stored in `backend/cache/sessions.json`
- To get a valid token for testing:
```bash
# Extract a valid token from sessions file
cat backend/cache/sessions.json | head -10
# Look for "token" field in the JSON
```

**Public endpoints (no auth required):**
- `/health` - Health check
- `/api/version` - Version info
- `/api/auth/default-password` - Check if default password set
- `/api/auth/login` - Login endpoint
- `/api/static/*` - Static assets
- `/api/debug/*` - Debug endpoints (localhost only)

**Example authenticated request:**
```bash
# With Authorization header
curl -H "Authorization: Bearer YOUR_TOKEN" http://localhost:7777/api/settings

# With query parameter
curl "http://localhost:7777/api/video/stream?token=YOUR_TOKEN&url=..."
```

**Permission levels:**
- Regular auth: Most endpoints (`/api/settings` GET, `/api/search`, `/api/discover/*`, etc.)
- Master only: Admin endpoints (`/api/settings` PUT, `/api/accounts/*`, `/api/admin/*`)
- Profile-scoped: User endpoints (`/api/users/{userID}/*` - users can only access their own profiles)
