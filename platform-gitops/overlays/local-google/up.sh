#!/usr/bin/env bash
# Switches the platform to the REAL Google login (overlay local-google). Requires overlays/local/up.sh to have run
# (DB, secrets, hello apps, Traefik). Idempotent. Back to the fake login: ./down.sh
#
#   platform-gitops/overlays/local-google/up.sh              # import the image already built on the host
#   BUILD=1 platform-gitops/overlays/local-google/up.sh      # build portal-platform-api:dev-google first
#
# The client secret lives in .tools/secrets/google-client-secret and is loaded with --from-file only.
# NOTE: overlays/local/up.sh applies the fake-login overlay again; re-run this script afterwards.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
REL="platform-gitops/overlays/local-google"
cd "$ROOT"

readonly IMAGE=portal-platform-api:dev-google
readonly SECRET_FILE=.tools/secrets/google-client-secret
readonly ROLLOUT_TIMEOUT=240s
kc() { ./tk kubectl "$@"; }

[[ -s "$SECRET_FILE" ]] || { echo "missing $SECRET_FILE" >&2; exit 1; }

if [[ "${BUILD:-0}" == 1 ]]; then
  echo "==> building $IMAGE"
  docker build -f platform/api/Dockerfile -t "$IMAGE" platform
fi
echo "==> importing $IMAGE"
./tk k3d image import "$IMAGE" -c portal

echo "==> client secret (platform/platform-google-client-secret)"
./tk sh -c "kubectl -n platform create secret generic platform-google-client-secret --from-file=secret=$SECRET_FILE --dry-run=client -o yaml | kubectl apply -f - >/dev/null"

echo "==> migrate Job"
kc -n platform delete job migrate --ignore-not-found
kc apply --dry-run=server -k "$REL/migrate" >/dev/null
kc apply -k "$REL/migrate"
kc -n platform wait --for=condition=complete job/migrate --timeout=180s

echo "==> platform-api (Google login)"
kc apply --dry-run=server -k "$REL/platform" >/dev/null
kc apply -k "$REL/platform"
if [[ "${BUILD:-0}" == 1 ]]; then
  # The tag does not change on rebuild, so the Deployment spec is identical and nothing rolls out by itself.
  kc -n platform rollout restart deploy/platform-api
fi
kc -n platform rollout status deploy/platform-api --timeout="$ROLLOUT_TIMEOUT"
echo "==> done. Users are created on first login; grant access with $REL/grant.sh"
