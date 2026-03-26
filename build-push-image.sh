#!/usr/bin/env bash
# Build (and optionally push) the herm base Docker image for multiple architectures.
# Usage: ./build-push-image.sh [--push] [--dry-run]

set -euo pipefail

# Read the image tag from the shared version file (single source of truth)
VERSION_FILE="cmd/herm/container_version"
if [ ! -f "$VERSION_FILE" ]; then
  echo "Error: ${VERSION_FILE} not found (run from repo root)" >&2
  exit 1
fi
TAG=$(tr -d '[:space:]' < "$VERSION_FILE")

IMAGE="aduermael/herm:${TAG}"
PLATFORMS="linux/amd64,linux/arm64"
PUSH=false
DRY_RUN=false

for arg in "$@"; do
  case "$arg" in
    --push)    PUSH=true ;;
    --dry-run) DRY_RUN=true ;;
    *) echo "Unknown flag: $arg" >&2; exit 1 ;;
  esac
done

echo "Image:     ${IMAGE}"
echo "Platforms: ${PLATFORMS}"

PUSH_FLAG=""
if [ "$PUSH" = true ]; then
  PUSH_FLAG="--push"
else
  PUSH_FLAG="--load"
fi

if [ "$DRY_RUN" = true ]; then
  echo "(dry-run) Would run:"
  echo "  docker buildx build --platform ${PLATFORMS} -t ${IMAGE} ${PUSH_FLAG} ."
  exit 0
fi

# Ensure a buildx builder with multi-arch support exists
BUILDER="herm-multiarch"
if ! docker buildx inspect "$BUILDER" &>/dev/null; then
  echo "Creating buildx builder: ${BUILDER}"
  docker buildx create --name "$BUILDER" --driver docker-container --use
else
  docker buildx use "$BUILDER"
fi

docker buildx build --platform "$PLATFORMS" -t "$IMAGE" ${PUSH_FLAG} .

if [ "$PUSH" = true ]; then
  echo "Pushed ${IMAGE} for ${PLATFORMS}"
else
  echo "Built ${IMAGE} for ${PLATFORMS} (use --push to push to registry)"
fi
