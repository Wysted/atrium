#!/usr/bin/env bash
# Deploys the platform (PostgreSQL, platform-api, two hello apps) into the k3d cluster `portal`, seeds dev data,
# removes the forwardauth-cookie spike and enables the entrypoint ForwardAuth in Traefik.
# Idempotent. Cluster commands go through ./tk; only image builds use the host docker.
#
#   platform-gitops/overlays/local/up.sh              # import images already built on the host
#   BUILD=1 platform-gitops/overlays/local/up.sh      # rebuild both images first
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
kc() { ./tk kubectl "$@"; }

echo "==> secrets (generated once in $SECRETS)"
umask 077
mkdir -p "$SECRETS"
for f in k3d-pg-admin-password k3d-pg-owner-password k3d-pg-api-password; do
  [[ -s "$SECRETS/$f" ]] || openssl rand -hex "$PASSWORD_BYTES" | tr -d '\n' > "$SECRETS/$f"
done
if [[ ! -s "$SECRETS/k3d-platform-kek" ]]; then
  openssl rand "$KEK_BYTES" > "$SECRETS/k3d-platform-kek"
fi
chmod 0400 "$SECRETS/k3d-platform-kek"
# printf is a shell builtin: passwords never show up in an argument list.
printf '%s:5432:platform:platform_owner:%s\n' "$DB_HOST" "$(<"$SECRETS/k3d-pg-owner-password")" > "$SECRETS/k3d-owner-pgpass"
printf '%s:5432:platform:platform_api:%s\n' "$DB_HOST" "$(<"$SECRETS/k3d-pg-api-password")" > "$SECRETS/k3d-api-pgpass"

if [[ "${BUILD:-0}" == 1 ]]; then
  echo "==> building images"
  docker build -f platform/api/Dockerfile -t portal-platform-api:dev platform
  docker build -f platform/examples/hello/Dockerfile -t portal-hello:dev platform
fi
echo "==> importing images"
./tk k3d image import portal-platform-api:dev portal-hello:dev -c portal

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
  admin-password="$SECRETS/k3d-pg-admin-password" owner-password="$SECRETS/k3d-pg-owner-password" api-password="$SECRETS/k3d-pg-api-password"
secret platform platform-kek kek="$SECRETS/k3d-platform-kek"
secret platform platform-owner-pgpass pgpass="$SECRETS/k3d-owner-pgpass"
secret platform platform-api-pgpass pgpass="$SECRETS/k3d-api-pgpass"

echo "==> PostgreSQL"
kc apply --dry-run=server -k "$REL/db" >/dev/null
kc apply -k "$REL/db"
kc -n platform-db rollout status statefulset/postgres --timeout="$ROLLOUT_TIMEOUT"

echo "==> migrate Job (as platform_owner)"
kc -n platform delete job migrate --ignore-not-found
kc apply --dry-run=server -k "$REL/migrate" >/dev/null
kc apply -k "$REL/migrate"
if ! kc -n platform wait --for=condition=complete job/migrate --timeout=180s; then
  kc -n platform logs job/migrate --tail=50 || true
  echo "migrate failed" >&2; exit 1
fi
kc -n platform logs job/migrate --tail=20

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

echo "==> seed"
"$HERE/seed.sh"

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
