#!/bin/bash
set -euo pipefail

# Build and push multi-arch Docker image locally
# Replicates the GitHub Actions docker-build workflow

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGE="godver3/mediastorm"
DOCKERFILE="backend/Dockerfile"

# Read version (line 1 = semantic version, line 2 = build ID)
VERSION=$(sed -n '1p' "$REPO_ROOT/backend/version.txt" | tr -d '[:space:]')
COMMIT_SHA=$(git -C "$REPO_ROOT" rev-parse HEAD)

echo "=== Docker Build ==="
echo "Version:  $VERSION"
echo "Commit:   $COMMIT_SHA"
echo "Image:    $IMAGE"
echo ""

# Parse flags
PUSH=false
PLATFORMS="linux/amd64,linux/arm64"
KEEP_BUILDER=false
BUILDER_NAME=""
for arg in "$@"; do
    case "$arg" in
        --push) PUSH=true ;;
        --amd64) PLATFORMS="linux/amd64" ;;
        --arm64) PLATFORMS="linux/arm64" ;;
        --keep-builder) KEEP_BUILDER=true ;;
        --builder=*) BUILDER_NAME="${arg#*=}" ;;
        --help|-h)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --push     Push images to Docker Hub after building"
            echo "  --amd64    Build only linux/amd64 (faster for testing)"
            echo "  --arm64    Build only linux/arm64"
            echo "  --keep-builder"
            echo "             Keep the buildx builder after the build finishes"
            echo "  --builder=NAME"
            echo "             Use the specified buildx builder name"
            echo "  -h,--help  Show this help"
            echo ""
            echo "By default, builds linux/amd64 and linux/arm64 without pushing."
            exit 0
            ;;
        *)
            echo "Unknown option: $arg"
            exit 1
            ;;
    esac
done

BUILDER_CREATED=false
cleanup() {
    if [[ "$BUILDER_CREATED" == "true" && "$KEEP_BUILDER" != "true" ]]; then
        echo ""
        echo "Removing temporary buildx builder: $BUILDER_NAME"
        docker buildx rm --force "$BUILDER_NAME" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

# Use a temporary builder by default so build cache state does not accumulate
# indefinitely between local runs.
if [[ -z "$BUILDER_NAME" ]]; then
    BUILDER_NAME="mediastorm-builder-$(date +%s)-$$"
fi

if docker buildx inspect "$BUILDER_NAME" &>/dev/null; then
    echo "Using existing buildx builder: $BUILDER_NAME"
    docker buildx use "$BUILDER_NAME"
else
    echo "Creating buildx builder: $BUILDER_NAME"
    docker buildx create --name "$BUILDER_NAME" --use --driver docker-container >/dev/null
    BUILDER_CREATED=true
fi

TAGS=(
    "-t" "$IMAGE:latest"
    "-t" "$IMAGE:$VERSION"
    "-t" "$IMAGE:$COMMIT_SHA"
)

BUILD_CMD=(
    docker buildx build
    --platform "$PLATFORMS"
    -f "$DOCKERFILE"
    "${TAGS[@]}"
)

if $PUSH; then
    echo "Will build and push to Docker Hub"
    echo ""

    # Verify Docker Hub login (check credential store config)
    if ! cat ~/.docker/config.json 2>/dev/null | grep -q "credsStore\|auths"; then
        echo "Not logged in to Docker Hub. Run: docker login"
        exit 1
    fi

    BUILD_CMD+=(--push)
else
    echo "Building locally (use --push to push to Docker Hub)"
    echo ""
    BUILD_CMD+=(--load)

    # --load only supports single platform
    if [[ "$PLATFORMS" == *","* ]]; then
        echo "Note: --load requires a single platform. Building amd64 only."
        echo "Use --push for multi-arch builds, or --amd64/--arm64 for a specific platform."
        PLATFORMS="linux/amd64"
        BUILD_CMD=()
        BUILD_CMD=(
            docker buildx build
            --platform "$PLATFORMS"
            -f "$DOCKERFILE"
            "${TAGS[@]}"
            --load
        )
    fi
fi

echo "Platforms: $PLATFORMS"
echo "Tags:      $IMAGE:latest, $IMAGE:$VERSION, $IMAGE:$COMMIT_SHA"
echo ""

cd "$REPO_ROOT"
"${BUILD_CMD[@]}" .

echo ""
echo "=== Build complete ==="
if $PUSH; then
    echo "Pushed: $IMAGE:latest, $IMAGE:$VERSION, $IMAGE:$COMMIT_SHA"

    # Create or update version tag
    TAG_NAME="v$VERSION"
    if git rev-parse "$TAG_NAME" &>/dev/null 2>&1; then
        echo "Updating git tag: $TAG_NAME"
        git tag -f "$TAG_NAME"
        git push mediastorm "$TAG_NAME" --force
    else
        echo "Creating git tag: $TAG_NAME"
        git tag "$TAG_NAME"
        git push mediastorm "$TAG_NAME"
    fi
else
    echo "Built locally: $IMAGE:latest, $IMAGE:$VERSION, $IMAGE:$COMMIT_SHA"
fi
