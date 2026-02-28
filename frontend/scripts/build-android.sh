#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

usage() {
  echo "Usage: $0 <mobile|tv|both>"
  echo ""
  echo "Build Android APKs and upload to GitHub release:"
  echo "  mobile - Build Android mobile APK and upload"
  echo "  tv     - Build Android TV APK and upload"
  echo "  both   - Build and upload both (mobile first, then TV)"
  exit 1
}

# Read version from version.ts
get_version() {
  local version
  version=$(grep -o "APP_VERSION\s*=\s*'[^']*'" "$PROJECT_ROOT/version.ts" | grep -o "'[^']*'" | tr -d "'")
  if [ -z "$version" ]; then
    echo "Error: Could not read APP_VERSION from version.ts"
    exit 1
  fi
  echo "$version"
}

# Create or update GitHub release, then upload asset (replacing if it exists)
upload_to_release() {
  local version="$1"
  local tag="v${version}"
  local apk_path="$2"
  local asset_name="$3"

  # Create release if it doesn't exist
  if ! gh release view "$tag" &>/dev/null; then
    echo "  Creating release $tag..."
    gh release create "$tag" \
      --title "$tag" \
      --notes "$(cat <<EOF
## Release ${version}

### Downloads
- **mediastorm-mobile-${version}.apk** - Android Mobile version
- **mediastorm-tv-${version}.apk** - Android TV version
EOF
)"
  fi

  # Delete existing asset if present
  if gh release view "$tag" --json assets --jq '.assets[].name' 2>/dev/null | grep -qx "$asset_name"; then
    echo "  Replacing existing asset $asset_name..."
    gh release delete-asset "$tag" "$asset_name" --yes
  fi

  echo "  Uploading $asset_name..."
  gh release upload "$tag" "$apk_path#$asset_name"
}

build_mobile() {
  local version
  version=$(get_version)

  echo "========================================"
  echo " Building Android Mobile (v${version})"
  echo "========================================"
  echo ""

  echo "-> Running EAS local build (production profile)..."
  eas build --platform android --profile production --local

  # Find the most recent .apk
  local apk
  apk=$(ls -t build-*.apk 2>/dev/null | head -1)
  if [ -z "$apk" ]; then
    echo "Error: No APK file found after build"
    exit 1
  fi

  echo ""
  echo "-> Uploading to GitHub release..."
  upload_to_release "$version" "$apk" "mediastorm-mobile-${version}.apk"

  rm -f "$apk"

  echo ""
  echo "Android mobile build uploaded!"
  echo ""
}

build_tv() {
  local version
  version=$(get_version)

  echo "========================================"
  echo " Building Android TV (v${version})"
  echo "========================================"
  echo ""

  echo "-> Running EAS local build (production_tv profile)..."
  eas build --platform android --profile production_tv --local

  # Find the most recent .apk
  local apk
  apk=$(ls -t build-*.apk 2>/dev/null | head -1)
  if [ -z "$apk" ]; then
    echo "Error: No APK file found after build"
    exit 1
  fi

  echo ""
  echo "-> Uploading to GitHub release..."
  upload_to_release "$version" "$apk" "mediastorm-tv-${version}.apk"

  rm -f "$apk"

  echo ""
  echo "Android TV build uploaded!"
  echo ""
}

# Main
[ $# -lt 1 ] && usage

case "$1" in
  mobile)
    build_mobile
    ;;
  tv)
    build_tv
    ;;
  both)
    build_mobile
    build_tv
    ;;
  *)
    usage
    ;;
esac

echo "Done! Release: https://github.com/$(gh repo view --json nameWithOwner -q .nameWithOwner)/releases/tag/v$(get_version)"
