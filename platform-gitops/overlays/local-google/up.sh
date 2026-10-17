#!/usr/bin/env bash
# Switches the platform to the REAL Google login (overlay local-google). Requires overlays/local/up.sh to have run
# (DB, secrets, hello apps, Traefik). Idempotent. Back to the fake login: ./down.sh
#
#   platform-gitops/overlays/local-google/up.sh              # import the image already built on the host
#   BUILD=1 platform-gitops/overlays/local-google/up.sh      # build portal-platform-api:dev-google first
#
# The client secret lives in .tools/secrets/google-client-secret and is loaded with --from-file only.
# The client id (public) lives in .tools/secrets/google-client-id and goes into a ConfigMap the same way.
# NOTE: overlays/local/up.sh applies the fake-login overlay again; re-run this script afterwards.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
REL="platform-gitops/overlays/local-google"
cd "$ROOT"

readonly IMAGE=portal-platform-api:dev-google
readonly SECRET_FILE=.tools/secrets/google-client-secret
readonly CLIENT_ID_FILE=.tools/secrets/google-client-id
readonly ROLLOUT_TIMEOUT=240s
kc() { ./tk kubectl "$@"; }

for f in "$SECRET_FILE" "$CLIENT_ID_FILE"; do
  [[ -s "$f" ]] || { echo "missing $f" >&2; exit 1; }
done

if [[ "${BUILD:-0}" == 1 ]]; then
  echo "==> building $IMAGE"
  docker build -f platform/api/Dockerfile -t "$IMAGE" platform
# Warn (never fail) when the host image predates the build inputs: the tag never changes, so a stale image is silent.
# Context = all of platform/ except docs and *.md (platform/.dockerignore); uncommitted changes count as newer.
elif ! created="$(docker image inspect -f '{{.Created}}' "$IMAGE" 2>/dev/null)"; then
  echo "WARN: $IMAGE not found on the host; run with BUILD=1" >&2
elif [[ -n "$(git status --porcelain -- platform ':!platform/docs' ':!*.md')" ]] ||
  (( $(date -d "$created" +%s) < $(git log -1 --format=%ct -- platform ':!platform/docs' ':!*.md' | grep . || echo 0) )); then
  echo "WARN: $IMAGE is older than the platform/ sources (or they have uncommitted changes); run with BUILD=1 if it should include them" >&2
fi
echo "==> importing $IMAGE"
./tk k3d image import "$IMAGE" -c portal

echo "==> client secret (platform/platform-google-client-secret)"
./tk sh -c "kubectl -n platform create secret generic platform-google-client-secret --from-file=secret=$SECRET_FILE --dry-run=client -o yaml | kubectl apply -f - >/dev/null"

echo "==> client id (platform/platform-google-client-id)"
./tk sh -c "kubectl -n platform create configmap platform-google-client-id --from-file=client-id=$CLIENT_ID_FILE --dry-run=client -o yaml | kubectl apply -f - >/dev/null"

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
echo "==> done. Users are created on first login; make the first platform admin with $REL/admin.sh, then grant access in the portal or with portalctl (see README)"
