#!/usr/bin/env bash
# Start the isolated admin-dev stack on :7778 (own Postgres + cache).
# Does not touch the live Dockge mediastorm container on :7777.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mkdir -p .dev-data/cache

echo "Building and starting mediastorm-admin-dev on http://localhost:7778 ..."
docker compose -p mediastorm-admin-dev -f docker-compose.admin-dev.yml up -d --build

echo ""
echo "Admin Settings: http://localhost:7778/admin/settings"
echo "Tear down with: docker compose -p mediastorm-admin-dev -f docker-compose.admin-dev.yml down"
echo "Live stack on :7777 is untouched."
