#!/usr/bin/env bash
# Create the local cluster and install Traefik. Run inside the toolbox:
#   ./tk local/cluster-up.sh
set -euo pipefail

readonly CLUSTER=portal
readonly API_SERVER="https://k3d-$CLUSTER-serverlb:6443"
readonly TRAEFIK_CHART_VERSION=41.7.1
readonly DIR="$(cd "$(dirname "$0")" && pwd)"

if ! k3d cluster list "$CLUSTER" >/dev/null 2>&1; then
  k3d cluster create --config "$DIR/k3d.yaml"
fi

# k3d writes the loopback host port; this container reaches the API over portal-net instead.
kubectl config set-cluster "k3d-$CLUSTER" --server="$API_SERVER" >/dev/null
kubectl wait --for=condition=Ready node --all --timeout=120s

helm repo add traefik https://traefik.github.io/charts >/dev/null 2>&1 || true
helm repo update traefik >/dev/null
helm upgrade --install traefik traefik/traefik \
  --version "$TRAEFIK_CHART_VERSION" \
  --namespace traefik --create-namespace \
  --values "$DIR/traefik-values.yaml" \
  --wait --timeout 5m

kubectl -n traefik get pods,svc
