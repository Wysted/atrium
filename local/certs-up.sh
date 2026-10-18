#!/usr/bin/env bash
# Install cert-manager and issue the wildcard certificate. Run inside the toolbox:
#   ./tk local/certs-up.sh            (Let's Encrypt staging)
#   ./tk local/certs-up.sh prod       (Let's Encrypt production)
#   ./tk local/certs-up.sh local      (local CA, no Cloudflare token needed)
# With no argument and without .tools/secrets/cloudflare-token the local CA is used; an explicit staging/prod never falls back. In that mode the CA certificate is
# exported to .tools/secrets/local-ca.crt (public data) so smoke.sh and browser-check trust it; the Let's Encrypt modes
# delete that file, which is how the other scripts know which mode the cluster is in.
set -euo pipefail

readonly CERT_MANAGER_CHART_VERSION=v1.21.2
readonly DIR="$(cd "$(dirname "$0")" && pwd)"
readonly TOKEN_FILE="$DIR/../.tools/secrets/cloudflare-token"
readonly LOCAL_CA_FILE="$DIR/../.tools/secrets/local-ca.crt"
# No argument: staging with the maintainer's token, the local CA without it. An explicit mode never falls back.
MODE="${1:-}"
if [[ -z "$MODE" ]]; then
  if [[ -s "$TOKEN_FILE" ]]; then MODE=staging; else MODE=local; echo "no $TOKEN_FILE: using the local CA (mode local)" >&2; fi
fi
case "$MODE" in
  local) export ISSUER=local-ca ;;
  staging | prod)
    [[ -s "$TOKEN_FILE" ]] || { echo "missing $TOKEN_FILE; use 'local'" >&2; exit 1; }
    export ISSUER="letsencrypt-$MODE" ;;
  *) echo "usage: certs-up.sh [local|staging|prod]" >&2; exit 2 ;;
esac

# wait_issued: the Certificate may still be Ready for the previous issuer, so first wait until the Secret was issued by $ISSUER.
wait_issued() {
  local end=$((SECONDS + 300)) got
  while ((SECONDS < end)); do
    got="$(kubectl -n traefik get secret apps-wildcard-tls -o jsonpath='{.metadata.annotations.cert-manager\.io/issuer-name}' 2>/dev/null || true)"
    [[ "$got" == "$ISSUER" ]] && break
    sleep 3
  done
  [[ "$got" == "$ISSUER" ]] || { echo "apps-wildcard-tls was not issued by $ISSUER within 5 min" >&2; exit 1; }
  kubectl -n traefik wait --for=condition=Ready certificate/apps-wildcard --timeout=5m
}

helm repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
helm repo update jetstack >/dev/null
helm upgrade --install cert-manager jetstack/cert-manager \
  --version "$CERT_MANAGER_CHART_VERSION" \
  --namespace cert-manager --create-namespace \
  --values "$DIR/cert-manager/values.yaml" \
  --wait --timeout 5m

rm -f "$LOCAL_CA_FILE" # rewritten only by a successful local run, so a failed run never leaves a stale CA behind
if [[ "$MODE" == local ]]; then
  kubectl apply -f "$DIR/cert-manager/local-ca.yaml"
  kubectl -n cert-manager wait --for=condition=Ready certificate/local-ca --timeout=2m
  kubectl wait --for=condition=Ready clusterissuer/local-ca --timeout=2m
  envsubst '${ISSUER}' < "$DIR/cert-manager/wildcard-cert.yaml" | kubectl apply -f -
  wait_issued
  umask 022
  kubectl -n cert-manager get secret local-ca-tls -o jsonpath='{.data.tls\.crt}' | base64 -d > "$LOCAL_CA_FILE"
  echo "local CA exported to .tools/secrets/local-ca.crt"
  kubectl -n traefik get certificate apps-wildcard
  exit 0
fi

# The token never touches the command line: read from file, whitespace stripped.
kubectl -n cert-manager create secret generic cloudflare-api-token \
  --from-file=api-token=<(tr -d '[:space:]' < "$TOKEN_FILE") \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f "$DIR/cert-manager/issuers.yaml"
envsubst '${ISSUER}' < "$DIR/cert-manager/wildcard-cert.yaml" | kubectl apply -f -

wait_issued
kubectl -n traefik get certificate apps-wildcard
