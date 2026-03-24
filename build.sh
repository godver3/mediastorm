#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$REPO_ROOT/backend"
FRONTEND_DIR="$REPO_ROOT/frontend"

usage() {
  cat <<'EOF'
Usage:
  ./build.sh all [docker flags...]
  ./build.sh docker [docker flags...]
  ./build.sh appstore <ios|tvos|both>
  ./build.sh android <mobile|tv|both>
  ./build.sh backend all [docker flags...]
  ./build.sh backend docker [docker flags...]
  ./build.sh frontend appstore <ios|tvos|both>
  ./build.sh frontend android <mobile|tv|both>

Examples:
  ./build.sh all --push
  ./build.sh docker --push
  ./build.sh backend docker --push --amd64
  ./build.sh appstore both
  ./build.sh frontend android both

Startup sequence:
  - macOS login keychain must be unlocked
  - root repo must be clean and synced with its upstream
  - frontend repo must be clean and synced with its upstream
  - backend/frontend build IDs are stamped to today's date
EOF
}

error() {
  echo "Error: $*" >&2
  exit 1
}

check_keychain_unlocked() {
  local keychain_path="$HOME/Library/Keychains/login.keychain-db"

  if [[ ! -e "$keychain_path" ]]; then
    keychain_path="$HOME/Library/Keychains/login.keychain"
  fi

  [[ -e "$keychain_path" ]] || error "Could not find your login keychain."

  if ! security show-keychain-info "$keychain_path" >/dev/null 2>&1; then
    error "Your login keychain appears locked. Unlock it first, then rerun. Example: security unlock-keychain '$keychain_path'"
  fi
}

check_repo_clean_and_synced() {
  local repo_path="$1"
  local label="$2"
  local branch upstream remote status ahead behind

  branch=$(git -C "$repo_path" rev-parse --abbrev-ref HEAD)
  [[ "$branch" != "HEAD" ]] || error "$label repo is in detached HEAD state."

  status=$(git -C "$repo_path" status --porcelain)
  [[ -z "$status" ]] || error "$label repo has uncommitted or untracked changes. Commit, stash, or clean them first."

  if ! upstream=$(git -C "$repo_path" rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null); then
    error "$label repo branch '$branch' does not track an upstream branch."
  fi

  remote="${upstream%%/*}"
  [[ -n "$remote" ]] || error "Could not determine remote for $label repo."

  echo "Checking $label repo against $upstream..."
  git -C "$repo_path" fetch --prune "$remote"

  read -r behind ahead < <(git -C "$repo_path" rev-list --left-right --count "HEAD...$upstream")

  if [[ "$ahead" -gt 0 ]]; then
    error "$label repo has $ahead local commit(s) not pushed to $upstream."
  fi

  if [[ "$behind" -gt 0 ]]; then
    error "$label repo is behind $upstream by $behind commit(s). Pull/rebase first."
  fi
}

run_preflight() {
  echo "Running preflight checks..."
  check_keychain_unlocked
  check_repo_clean_and_synced "$REPO_ROOT" "root"
  check_repo_clean_and_synced "$FRONTEND_DIR" "frontend"
  echo "Preflight checks passed."
  echo ""
}

stamp_backend_version() {
  local version_file="$BACKEND_DIR/version.txt"
  local version build_id tmp_file

  [[ -f "$version_file" ]] || error "Missing backend version file: $version_file"

  version=$(sed -n '1p' "$version_file" | tr -d '[:space:]')
  [[ -n "$version" ]] || error "Could not read backend version from $version_file"

  build_id=$(date +%Y%m%d)
  tmp_file="$(mktemp)"
  printf '%s\n%s\n' "$version" "$build_id" > "$tmp_file"
  mv "$tmp_file" "$version_file"

  echo "Stamped backend/version.txt build ID: $build_id"
}

stamp_frontend_version() {
  local version_file="$FRONTEND_DIR/version.ts"
  local build_id

  [[ -f "$version_file" ]] || error "Missing frontend version file: $version_file"

  build_id=$(date +%Y%m%d)

  if ! sed -i '' "s/^export const BUILD_ID = '.*'/export const BUILD_ID = '${build_id}'/" "$version_file"; then
    error "Failed to stamp frontend BUILD_ID in $version_file"
  fi

  echo "Stamped frontend/version.ts BUILD_ID: $build_id"
}

stamp_versions() {
  echo "Stamping version files..."
  stamp_backend_version
  stamp_frontend_version
  echo ""
}

normalize_args() {
  local scope="${1:-}"
  local command="${2:-}"

  case "$scope" in
    all)
      printf '%s\n' "root" "all"
      return 0
      ;;
    docker)
      printf '%s\n' "backend" "docker"
      return 0
      ;;
    appstore|android)
      printf '%s\n' "frontend" "$scope"
      return 0
      ;;
    backend)
      case "$command" in
        all|docker)
          printf '%s\n' "$scope" "$command"
          return 0
          ;;
        *)
          error "Unknown backend command: ${command:-<missing>}"
          ;;
      esac
      ;;
    frontend)
      case "$command" in
        appstore|android)
          printf '%s\n' "$scope" "$command"
          return 0
          ;;
        *)
          error "Unknown frontend command: ${command:-<missing>}"
          ;;
      esac
      ;;
    *)
      usage
      exit 1
      ;;
  esac
}

dispatch() {
  local scope="$1"
  local command="$2"
  shift 2

  case "$scope:$command" in
    root:all|backend:all)
      "$BACKEND_DIR/scripts/build-docker.sh" "$@"
      "$FRONTEND_DIR/scripts/build-appstore.sh" both
      "$FRONTEND_DIR/scripts/build-android.sh" both
      ;;
    backend:docker)
      "$BACKEND_DIR/scripts/build-docker.sh" "$@"
      ;;
    frontend:appstore)
      [[ $# -ge 1 ]] || error "Missing appstore target. Expected one of: ios, tvos, both."
      "$FRONTEND_DIR/scripts/build-appstore.sh" "$@"
      ;;
    frontend:android)
      [[ $# -ge 1 ]] || error "Missing android target. Expected one of: mobile, tv, both."
      "$FRONTEND_DIR/scripts/build-android.sh" "$@"
      ;;
    *)
      error "Unsupported command: $scope $command"
      ;;
  esac
}

main() {
  [[ $# -ge 1 ]] || {
    usage
    exit 1
  }

  case "$1" in
    -h|--help|help)
      usage
      exit 0
      ;;
  esac

  local normalized
  mapfile -t normalized < <(normalize_args "${1:-}" "${2:-}")

  local scope="${normalized[0]}"
  local command="${normalized[1]}"
  local shift_count=1

  if [[ "$1" == "backend" || "$1" == "frontend" ]]; then
    shift_count=2
  fi

  run_preflight
  stamp_versions

  shift "$shift_count"
  dispatch "$scope" "$command" "$@"
}

main "$@"
