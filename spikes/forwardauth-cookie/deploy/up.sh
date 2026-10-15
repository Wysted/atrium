#!/usr/bin/env bash
# Build, import, deploy the forwardauth-cookie spike and enable the entrypoint ForwardAuth.
# Run from anywhere with the host docker available; cluster commands go through ./tk.
set -euo pipefail

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="$(cd "$SPIKE_DIR/../.." && pwd)"
REL="spikes/forwardauth-cookie/deploy"
cd "$ROOT"

echo "==> building images"
docker build --build-arg BINARY=authz -t portal-spike-authz:dev "$SPIKE_DIR"
docker build --build-arg BINARY=app -t portal-spike-app:dev "$SPIKE_DIR"

echo "==> importing images into k3d cluster 'portal'"
./tk k3d image import portal-spike-authz:dev portal-spike-app:dev -c portal

echo "==> applying manifests (namespaces first, then the rest)"
./tk kubectl apply -f "$REL/namespaces.yaml"
./tk kubectl apply -k "$REL"
# Re-roll so a re-imported image with the same tag is picked up.
./tk kubectl -n spike-authz rollout restart deploy/authz
./tk kubectl -n spike-authz rollout status deploy/authz --timeout=120s
for n in app1 app2; do
  ./tk kubectl -n "spike-$n" rollout restart deploy/app
  ./tk kubectl -n "spike-$n" rollout status deploy/app --timeout=180s
done

# The Middleware must exist before Traefik references it at entrypoint level.
echo "==> enabling ForwardAuth at the websecure entrypoint"
./tk kubectl -n spike-authz get middleware.traefik.io forwardauth
./tk helm upgrade traefik traefik/traefik --version 41.7.1 -n traefik \
  -f local/traefik-values.yaml -f "$REL/traefik-entrypoint-values.yaml"
./tk kubectl -n traefik rollout status deploy/traefik --timeout=180s

echo "==> done. Run $REL/smoke.sh"
echo "    rollback: ./tk helm upgrade traefik traefik/traefik --version 41.7.1 -n traefik -f local/traefik-values.yaml"
