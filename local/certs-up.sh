#!/usr/bin/env bash
# Install cert-manager and issue the wildcard certificate. Run inside the toolbox:
#   ./tk local/certs-up.sh            (Let's Encrypt staging)
#   ./tk local/certs-up.sh prod       (Let's Encrypt production)
set -euo pipefail

readonly CERT_MANAGER_CHART_VERSION=v1.21.2
readonly DIR="$(cd "$(dirname "$0")" && pwd)"
readonly TOKEN_FILE="$DIR/../.tools/secrets/cloudflare-token"
export ISSUER="letsencrypt-${1:-staging}"

[[ -s "$TOKEN_FILE" ]] || { echo "missing $TOKEN_FILE" >&2; exit 1; }

helm repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
helm repo update jetstack >/dev/null
helm upgrade --install cert-manager jetstack/cert-manager \
  --version "$CERT_MANAGER_CHART_VERSION" \
  --namespace cert-manager --create-namespace \
  --values "$DIR/cert-manager/values.yaml" \
  --wait --timeout 5m

# The token never touches the command line: read from file, whitespace stripped.
kubectl -n cert-manager create secret generic cloudflare-api-token \
  --from-file=api-token=<(tr -d '[:space:]' < "$TOKEN_FILE") \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f "$DIR/cert-manager/issuers.yaml"
envsubst '${ISSUER}' < "$DIR/cert-manager/wildcard-cert.yaml" | kubectl apply -f -

kubectl -n traefik wait --for=condition=Ready certificate/apps-wildcard --timeout=5m
kubectl -n traefik get certificate apps-wildcard
