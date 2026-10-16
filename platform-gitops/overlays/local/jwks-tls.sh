#!/usr/bin/env bash
# Local PKI for the platform-api JWKS listener (:8083, HTTPS only). Called by up.sh; safe to run on its own.
#
#   .tools/secrets/k3d-jwks-ca.{key,crt}   ECDSA P-256 CA (10 years); the key never leaves the host
#   .tools/secrets/k3d-jwks-tls.{key,crt}  leaf (90 days) with the in-cluster service name SANs
#
# Cluster side: Secret platform/platform-api-jwks-tls (kubernetes.io/tls) and ConfigMap platform-jwks-ca (key ca.crt) in
# every app namespace. Idempotent: the CA and the leaf are reused until they are close to expiry (or the leaf stops
# verifying against the CA); the cluster objects are re-applied on every run. Key material is never printed.
# openssl and kubectl run in the toolbox (./tk).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
cd "$ROOT"

readonly SECRETS=.tools/secrets
readonly CA_KEY=$SECRETS/k3d-jwks-ca.key CA_CRT=$SECRETS/k3d-jwks-ca.crt
readonly LEAF_KEY=$SECRETS/k3d-jwks-tls.key LEAF_CRT=$SECRETS/k3d-jwks-tls.crt
readonly CA_DAYS=3650 LEAF_DAYS=90
readonly LEAF_RENEW_BEFORE_S=$((30 * 86400))
# Renew the CA while a fresh leaf (LEAF_DAYS) plus its renewal window and a margin still fits inside it,
# so no leaf outlives its CA: 90 + 30 + 30 = 150 days.
readonly CA_MARGIN_DAYS=30
readonly CA_RENEW_BEFORE_S=$(((LEAF_DAYS + LEAF_RENEW_BEFORE_S / 86400 + CA_MARGIN_DAYS) * 86400))
readonly SERVICE=platform-api-jwks
readonly SANS="DNS:$SERVICE,DNS:$SERVICE.platform,DNS:$SERVICE.platform.svc,DNS:$SERVICE.platform.svc.cluster.local"
readonly APP_NAMESPACES=(hello-a hello-b portal)
# Verifiers load the CA once at start-up (LoadRootCAs): a new CA needs a restart. namespace/deployment pairs.
readonly VERIFIERS=(portal/portal hello-a/hello hello-b/hello)
NEW_CA=0
ossl() { ./tk openssl "$@"; }

umask 077
mkdir -p "$SECRETS"

if [[ ! -s $CA_KEY || ! -s $CA_CRT ]] || ! ossl x509 -in "$CA_CRT" -noout -checkend "$CA_RENEW_BEFORE_S" >/dev/null; then
  echo "    jwks-tls: new CA"
  NEW_CA=1
  rm -f "$CA_KEY" "$CA_CRT" "$LEAF_KEY" "$LEAF_CRT"
  ossl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$CA_KEY"
  ossl req -x509 -new -key "$CA_KEY" -sha256 -days "$CA_DAYS" -subj "/CN=portal local JWKS CA" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -addext "subjectKeyIdentifier=hash" -out "$CA_CRT"
  chmod 0400 "$CA_KEY"
fi

leaf_ok() {
  [[ -s $LEAF_KEY && -s $LEAF_CRT ]] || return 1
  ossl x509 -in "$LEAF_CRT" -noout -checkend "$LEAF_RENEW_BEFORE_S" >/dev/null || return 1
  ossl verify -CAfile "$CA_CRT" -verify_hostname "$SERVICE.platform.svc.cluster.local" "$LEAF_CRT" >/dev/null 2>&1 || return 1
  # the certificate must belong to the key on disk
  [[ "$(ossl x509 -in "$LEAF_CRT" -noout -pubkey)" == "$(ossl pkey -in "$LEAF_KEY" -pubout)" ]]
}
if leaf_ok; then
  echo "    jwks-tls: leaf reused ($(ossl x509 -in "$LEAF_CRT" -noout -enddate))"
else
  echo "    jwks-tls: issuing leaf ($LEAF_DAYS days)"
  mkdir -p .tools/tmp
  work="$(mktemp -d -p "$ROOT/.tools/tmp")"
  trap 'rm -rf "$work"' EXIT
  w="${work#"$ROOT/"}"
  printf 'basicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=%s\n' "$SANS" > "$work/ext"
  ossl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$LEAF_KEY"
  ossl req -new -key "$LEAF_KEY" -subj "/CN=$SERVICE" -out "$w/leaf.csr"
  ossl x509 -req -in "$w/leaf.csr" -CA "$CA_CRT" -CAkey "$CA_KEY" -CAcreateserial -CAserial "$w/ca.srl" -days "$LEAF_DAYS" \
    -sha256 -extfile "$w/ext" -out "$LEAF_CRT"
fi

echo "    jwks-tls: cluster objects"
# Order matters on CA rotation: CA ConfigMaps -> restart verifiers -> Secret (the new leaf). The pipes run inside the toolbox.
for ns in "${APP_NAMESPACES[@]}"; do
  ./tk sh -c "kubectl -n $ns create configmap platform-jwks-ca --from-file=ca.crt=$CA_CRT --dry-run=client -o yaml | kubectl apply -f - >/dev/null"
done
if ((NEW_CA)); then
  echo "    jwks-tls: new CA, restarting the verifiers before the new leaf is served"
  for v in "${VERIFIERS[@]}"; do
    ./tk kubectl -n "${v%/*}" rollout restart "deployment/${v#*/}" >/dev/null
  done
  for v in "${VERIFIERS[@]}"; do
    ./tk kubectl -n "${v%/*}" rollout status "deployment/${v#*/}" --timeout=180s >/dev/null
  done
fi
./tk sh -c "kubectl -n platform create secret tls platform-api-jwks-tls --cert=$LEAF_CRT --key=$LEAF_KEY --dry-run=client -o yaml | kubectl apply -f - >/dev/null"
