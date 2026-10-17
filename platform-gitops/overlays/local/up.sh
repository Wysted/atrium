#!/usr/bin/env bash
# Deploys the platform (PostgreSQL, platform-api, portal, two hello apps) into the k3d cluster `portal`, seeds dev data,
# installs the monitoring stack (Prometheus Operator, Prometheus, Grafana), removes the forwardauth-cookie spike and
# enables the entrypoint ForwardAuth in Traefik.
# Idempotent. Cluster commands go through ./tk; only image builds use the host docker.
#
#   platform-gitops/overlays/local/up.sh              # import images already built on the host
#   BUILD=1 platform-gitops/overlays/local/up.sh      # rebuild the images first
#
# Secrets live in .tools/secrets/k3d-* (dir 700, files 600; the KEK is 400), are generated once and loaded with
# --from-file. They never appear in manifests, arguments or output. Do NOT regenerate the KEK: signing keys sealed
# with it become unusable (KEKMismatch).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
REL="platform-gitops/overlays/local"
cd "$ROOT"

readonly SECRETS=.tools/secrets
readonly PASSWORD_BYTES=32
readonly KEK_BYTES=32
readonly DB_HOST=postgres.platform-db.svc.cluster.local
readonly ROLLOUT_TIMEOUT=240s
# Pinned: Prometheus v3.15.0, Operator v0.94.1 and Grafana 13.2.4 come with this chart; monitoring/test-rules.sh pins the
# same Prometheus for promtool. CRDs are installed by the chart on first install only (helm never upgrades CRDs).
readonly KPS_CHART=prometheus-community/kube-prometheus-stack
readonly KPS_VERSION=92.10.0
readonly KPS_REPO=https://prometheus-community.github.io/helm-charts
kc() { ./tk kubectl "$@"; }

echo "==> secrets (generated once in $SECRETS)"
umask 077
mkdir -p "$SECRETS"
for f in k3d-pg-admin-password k3d-pg-owner-password k3d-pg-api-password k3d-pg-portal-password; do
  [[ -s "$SECRETS/$f" ]] || openssl rand -hex "$PASSWORD_BYTES" | tr -d '\n' > "$SECRETS/$f"
done
if [[ ! -s "$SECRETS/k3d-platform-kek" ]]; then
  openssl rand "$KEK_BYTES" > "$SECRETS/k3d-platform-kek"
fi
chmod 0400 "$SECRETS/k3d-platform-kek"
# Grafana admin (local, reached by port-forward only). Generated once; never printed.
[[ -s "$SECRETS/k3d-grafana-admin-password" ]] || openssl rand -hex "$PASSWORD_BYTES" | tr -d '\n' > "$SECRETS/k3d-grafana-admin-password"
printf 'admin' > "$SECRETS/k3d-grafana-admin-user"
# printf is a shell builtin: passwords never show up in an argument list.
printf '%s:5432:platform:platform_owner:%s\n' "$DB_HOST" "$(<"$SECRETS/k3d-pg-owner-password")" > "$SECRETS/k3d-owner-pgpass"
printf '%s:5432:platform:platform_api:%s\n' "$DB_HOST" "$(<"$SECRETS/k3d-pg-api-password")" > "$SECRETS/k3d-api-pgpass"
printf '%s:5432:platform:portal_app:%s\n' "$DB_HOST" "$(<"$SECRETS/k3d-pg-portal-password")" > "$SECRETS/k3d-portal-pgpass"

if [[ "${BUILD:-0}" == 1 ]]; then
  echo "==> building images"
  docker build -f platform/api/Dockerfile -t portal-platform-api:dev platform
  docker build -f platform/examples/hello/Dockerfile -t portal-hello:dev platform
  docker build -f platform/api/Dockerfile.portal -t portal-portal:dev platform
fi
echo "==> importing images"
./tk k3d image import portal-platform-api:dev portal-hello:dev portal-portal:dev -c portal

secret() { # secret <ns> <name> <key=file>...
  local ns="$1" name="$2"; shift 2
  local args=()
  for kv in "$@"; do args+=("--from-file=$kv"); done
  ./tk sh -c "kubectl -n $ns create secret generic $name ${args[*]} --dry-run=client -o yaml | kubectl apply -f - >/dev/null"
  echo "    secret $ns/$name"
}

echo "==> namespaces"
kc apply --dry-run=server -k "$REL/namespaces" >/dev/null
kc apply -k "$REL/namespaces"

echo "==> secrets in cluster"
secret platform-db postgres-bootstrap \
  admin-password="$SECRETS/k3d-pg-admin-password" owner-password="$SECRETS/k3d-pg-owner-password" api-password="$SECRETS/k3d-pg-api-password" portal-password="$SECRETS/k3d-pg-portal-password"
secret platform platform-kek kek="$SECRETS/k3d-platform-kek"
secret platform platform-owner-pgpass pgpass="$SECRETS/k3d-owner-pgpass"
secret platform platform-api-pgpass pgpass="$SECRETS/k3d-api-pgpass"
secret portal portal-pgpass pgpass="$SECRETS/k3d-portal-pgpass"
secret monitoring grafana-admin admin-user="$SECRETS/k3d-grafana-admin-user" admin-password="$SECRETS/k3d-grafana-admin-password"

echo "==> JWKS internal TLS (local CA; Secret platform-api-jwks-tls, ConfigMap platform-jwks-ca per app namespace)"
"$HERE/jwks-tls.sh"

echo "==> PostgreSQL"
kc apply --dry-run=server -k "$REL/db" >/dev/null
kc apply -k "$REL/db"
kc -n platform-db rollout status statefulset/postgres --timeout="$ROLLOUT_TIMEOUT"

echo "==> portal database roles (as the bootstrap superuser; migration 0009 needs platform_portal to exist)"
"$HERE/roles.sh"

echo "==> migrate Job (as platform_owner)"
kc -n platform delete job migrate --ignore-not-found
kc apply --dry-run=server -k "$REL/migrate" >/dev/null
kc apply -k "$REL/migrate"
if ! kc -n platform wait --for=condition=complete job/migrate --timeout=180s; then
  kc -n platform logs job/migrate --tail=50 || true
  echo "migrate failed" >&2; exit 1
fi
kc -n platform logs job/migrate --tail=20

echo "==> seed (before platform-api: it checks PLATFORM_PORTAL_APP_ID at start-up)"
"$HERE/seed.sh"

echo "==> platform-api + middlewares + auth host route"
kc apply --dry-run=server -k "$REL/platform" >/dev/null
kc apply -k "$REL/platform"
kc -n platform rollout restart deploy/platform-api >/dev/null   # pick up a re-imported image with the same tag
kc -n platform rollout status deploy/platform-api --timeout="$ROLLOUT_TIMEOUT"

echo "==> hello-a, hello-b"
for n in a b; do
  kc apply --dry-run=server -k "$REL/hello-$n" >/dev/null
  kc apply -k "$REL/hello-$n"
  kc -n "hello-$n" rollout restart deploy/hello >/dev/null
  kc -n "hello-$n" rollout status deploy/hello --timeout="$ROLLOUT_TIMEOUT"
done

echo "==> portal"
kc apply --dry-run=server -k "$REL/portal" >/dev/null
kc apply -k "$REL/portal"
kc -n portal rollout restart deploy/portal >/dev/null
kc -n portal rollout status deploy/portal --timeout="$ROLLOUT_TIMEOUT"

echo "==> monitoring: NetworkPolicies and Grafana dashboard (ConfigMap mounted by the chart)"
kc apply --dry-run=server -k "$REL/monitoring" >/dev/null
kc apply -k "$REL/monitoring"
kc apply --dry-run=server -k platform-gitops/monitoring/dashboards >/dev/null
kc apply -k platform-gitops/monitoring/dashboards
# Egress to the Kubernetes API: one /32 per endpoint of the `kubernetes` Service (the K3s node in k3d), not the whole subnet.
api_ips="$(kc get endpointslices.discovery.k8s.io -l kubernetes.io/service-name=kubernetes -o jsonpath='{.items[*].endpoints[*].addresses[*]}')"
[[ -n "$api_ips" ]] || { echo "cannot find the Kubernetes API endpoints" >&2; exit 1; }
API_TO=""
for ip in $api_ips; do API_TO+="        - ipBlock: {cidr: $ip/32}"$'\n'; done
API_TO="${API_TO%$'\n'}"
echo "    kube-apiserver endpoints: $api_ips"
tpl="$(<"$REL/monitoring/networkpolicy-apiserver.tpl.yaml")"   # bash substitution: no envsubst on the host
printf '%s\n' "${tpl//\$\{API_TO\}/$API_TO}" | kc apply -f -

echo "==> monitoring: $KPS_CHART $KPS_VERSION (Prometheus Operator, Prometheus, Grafana; no Alertmanager)"
./tk helm repo add prometheus-community "$KPS_REPO" >/dev/null
./tk helm repo update prometheus-community >/dev/null
./tk helm upgrade --install monitoring "$KPS_CHART" --version "$KPS_VERSION" -n monitoring \
  -f "$REL/monitoring/values.yaml" --wait --timeout 5m --hide-notes
# The Prometheus StatefulSet is created by the operator after the release is ready.
kc -n monitoring wait --for=condition=Available prometheus/monitoring-prometheus --timeout="$ROLLOUT_TIMEOUT"

echo "==> monitoring: PodMonitors and PrometheusRule (need the CRDs the chart just installed)"
kc apply --dry-run=server -k platform-gitops/monitoring/operator >/dev/null
kc apply -k platform-gitops/monitoring/operator

# With reloadStrategy ProcessSignal (no --web.enable-lifecycle) the config-reloader's first reload check after a pod start
# can time out after ~3 minutes and only then starts watching the rule files, so rules applied right after a Prometheus
# restart appear late. Wait for them here so the smoke starts from a settled state (a warning, not a failure).
want_rules="$(grep -c '^ *- alert:' platform-gitops/monitoring/rules/portal-platform.rules.yaml)"
echo "    waiting (up to 10 min) for Prometheus to load $want_rules rules"
loaded=0; end=$(( $(date +%s) + 600 ))
while (( $(date +%s) < end )); do
  loaded="$(kc get --raw /api/v1/namespaces/monitoring/services/monitoring-prometheus:9090/proxy/api/v1/rules 2>/dev/null \
    | ./tk jq '[.data.groups[] | select(.name | startswith("portal-platform.")) | (.rules | length)] | add // 0' 2>/dev/null || echo 0)"
  [[ "$loaded" == "$want_rules" ]] && break
  sleep 5
done
[[ "$loaded" == "$want_rules" ]] && echo "    $loaded rules loaded" || echo "    WARNING: only $loaded of $want_rules rules loaded after 10 min" >&2

echo "==> spike teardown (its entrypoint middlewares must not outlive it)"
"$HERE/down-spike.sh"

# The Middlewares must exist before Traefik references them at entrypoint level.
echo "==> enabling ForwardAuth at the websecure entrypoint"
kc -n platform get middleware.traefik.io strip-assertion forwardauth
./tk helm upgrade traefik traefik/traefik --version 41.7.1 -n traefik \
  -f local/traefik-values.yaml -f "$REL/traefik/entrypoint-values.yaml"
kc -n traefik rollout status deploy/traefik --timeout="$ROLLOUT_TIMEOUT"

echo "==> done. Run $REL/smoke.sh"
echo "    rollback: ./tk helm upgrade traefik traefik/traefik --version 41.7.1 -n traefik -f local/traefik-values.yaml"
