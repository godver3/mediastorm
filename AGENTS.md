# Repository Guidelines

## Project Structure & Module Organization
- `backend/`: Go server (`main.go`) with HTTP handlers in `handlers/`, business logic in `services/`, shared/domain models in `models/`, and core packages in `internal/`.
- `frontend/`: Expo React Native app. Routes in `app/`, UI in `components/`, reusable logic in `hooks/` and `services/`, native modules in `modules/`, automation scripts in `scripts/`.
- `docs/`: planning and operational notes. `docs/TODO.md` tracks open work and is gitignored.
- `.github/workflows/`: Android artifact builds and backend Docker publish pipeline.

## Repository Boundaries
- Root (`/Users/liamhughes/strmr`) is the backend/docs/ops repo.
- `frontend/` is a separate Git repository with its own `.git` directory.
- `ksplayer/` is a gitignored symlink to a separate local clone at `~/ksplayer/` for iOS/tvOS native player work.
- Run frontend git commands from `frontend/` (or with `git -C frontend ...`), not from root.
- The root repo ignores `frontend/`, so frontend file changes will not stage from the root repo.
- Handle backend and frontend git operations separately. Root repo diffs/status/logs do not include frontend changes, and frontend repo commands do not include root changes.

## Build, Test, and Development Commands
- Backend (`cd backend`): `make run`, `make test`, `make check`, `make build`.
- Frontend (`cd frontend`): `npm ci`, `npm run start`, `npm run start:tv`, `npm run test`, `npm run lint`.
- Full stack local workflow (repo root): `./dev.sh start|stop|restart [backend|frontend]`.
- Debug logs: `.logs/backend.log` and `.logs/frontend.log` (example: `tail -f .logs/backend.log`).
- After backend Go changes, restart with `./dev.sh restart backend`; frontend JS/TS changes usually apply via hot reload.
- Local `go build` in `backend/` creates a `mediastorm` binary artifact that should not be kept.

## Coding Style & Naming Conventions
- Go: format with `go fmt ./...`; keep handlers thin and move logic into `services`.
- TypeScript/React Native: Prettier + ESLint are authoritative (`tabWidth: 2`, single quotes, semicolons, trailing commas).
- Naming: React components `PascalCase.tsx`, hooks `useFeature.ts`, tests as `*_test.go` or `*.test.ts(x)`.
- Prefer small, focused files; split oversized files proactively.

## Testing Guidelines
- Backend changes should include tests in the same package area (for example `handlers/*_test.go`, `services/*/service_test.go`, `internal/*_test.go`).
- Run `cd backend && go test ./...` before opening a PR.
- Run package-scoped backend tests when iterating quickly, for example `cd backend && go test ./handlers/... -v`.
- Frontend uses `jest-expo`; run `cd frontend && npm run test`.
- Validate TV-focused UX changes (focus order, remote navigation, playback transitions).
- For Go tests, keep tests in the same directory as the code under test and use `_test.go` suffixes.

## Commit & Pull Request Guidelines
- Use Conventional Commit style where possible (seen in history): `feat(frontend): ...`, `fix(backend): ...`, `style(frontend): ...`.
- PRs should include summary, rationale, related issue, and test evidence.
- Include screenshots or GIFs for UI changes, especially TV/mobile flows.

## Security, Config, and Operations
- Do not expose backend directly to the public internet; use private networking/VPN.
- Never commit secrets (API keys, tokens, credentials).
- Docker Hub image is `godver3/mediastorm`; build backend image from repo root with `-f backend/Dockerfile`.
- When editing `docs/TODO.md`, keep only open items and never include sensitive data (it is publicly served in local tooling).
- Most API endpoints require auth via `Authorization: Bearer <token>`, `X-PIN`, or `?token=`.
- For local API testing, you can retrieve a current session token from the Docker Postgres container with `docker exec mediastorm-postgres psql -U mediastorm -d mediastorm -Atc "SELECT token FROM sessions WHERE expires_at > now() ORDER BY created_at DESC LIMIT 1;"`.
- When testing authenticated APIs with `curl`, prefer `Authorization: Bearer <token>` and wrap header values in single quotes if the token contains shell-special characters.
- `docs/TODO.md` is gitignored. Mark active items as `In Progress Testing` with a short note, and only remove items after user confirmation that the fix works.
- Application settings, including API keys for local debugging, are stored in `backend/cache/settings.json`. `/api/settings` masks sensitive values.
- PostgreSQL is the sole datastore. Connection is via `DATABASE_URL`, and migrations live in `backend/internal/datastore/migrations/`.

## Debugging & Monitoring
- Check `.logs/backend.log` and `.logs/frontend.log` first when diagnosing issues.
- `monitor.sh` at repo root captures system metrics and backend runtime diagnostics into `.monitoring/` for instability or performance investigations.
- Local debug endpoints are available on the backend under `/api/debug/runtime` and `/api/debug/pprof/` for runtime stats, heap, and goroutine inspection.

## KSPlayer Notes
- The iOS/tvOS native player depends on the fork `godver3/KSPlayer` on branch `strmr-fixes`, cloned locally at `~/ksplayer/`.
- Make KSPlayer code changes in `~/ksplayer/`, not in this repo root.
- After KSPlayer changes, refresh native iOS dependencies with `cd frontend && npx expo prebuild --platform ios --clean`.
- The Expo config plugin wiring KSPlayer is `frontend/plugins/with-ksplayer.js`.
