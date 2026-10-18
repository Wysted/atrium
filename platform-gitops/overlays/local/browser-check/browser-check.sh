#!/usr/bin/env bash
# Real-browser check of the portal web app (Chromium via Playwright, in Docker): fake login (alice/carol/dave/admin), the home with its data, the Administration button, a client route
# without a page renders the error page; zero CSP violations, failed requests and (tolerated: NUXT_E1005) console errors.
# Called as the last section of smoke.sh; also runnable alone. Needs the host docker (the mechanism for images, see
# _shared/k8s.md), the cluster up and *.apps.wysted.dev -> 127.0.0.1 (the container instead resolves the two hosts to the
# k3d load balancer on portal-net, so no host port is involved).
# Hardening: the container runs as the host uid/gid with --cap-drop ALL, no-new-privileges and a private /dev/shm
# (--shm-size, not --ipc=host). The check directory is mounted read-only at /work; only node_modules/ and out/ (both
# git-ignored, created here as the host user so never root-owned) are writable overlays. The npm cache lives in
# .tools/npm/cache. Chromium needs no extra flag: Playwright launches it with its sandbox off (chromiumSandbox defaults to
# false), which is the documented way to run it as non-root in Docker without a custom seccomp profile; the container
# itself (non-root, no caps, read-only sources, throwaway) is the isolation boundary.
# Output: PASS/FAIL lines, screenshots in out/, exit 1 on any failure.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../../.." && pwd)"
# Keep the version of package.json / package-lock.json equal to this tag: the browsers baked into the image are the ones
# playwright 1.64.1 drives.
IMG=mcr.microsoft.com/playwright:v1.64.1-noble@sha256:7852dc5a7d72f5fe458a597d690f6c42fbc8e9e26ddfa95f8e6ef552558d9bbf
LB=k3d-portal-serverlb
NET=portal-net
# Unique per run, so the check proves a NEW unknown_host denial is recorded (rows live 7 days, one per host per 10 minutes). Single
# source of truth: rendered into the fixture's Host(), passed to the container (ORPHAN_HOST, --add-host). Declared by no app: refused as unknown_host.
ORPHAN_HOST="orphan-smoke-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n').apps.wysted.dev"
HOSTS=(portal.apps.wysted.dev auth.apps.wysted.dev grafana.apps.wysted.dev "$ORPHAN_HOST")

mkdir -p "$ROOT/.tools/npm/cache" "$HERE/out" "$HERE/node_modules"
LB_IP="$(docker inspect "$LB" --format "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}")"
[[ -n "$LB_IP" ]] || { echo "  FAIL: cannot resolve the IP of $LB on $NET (is the cluster up?)"; exit 1; }

# Local CA mode (local/certs-up.sh without a Cloudflare token exports this file; stale files are rejected): Node (the orphan
# fetch) trusts the CA through NODE_EXTRA_CA_CERTS. Chromium cannot add a root (the pinned image has no NSS tools to do it as
# non-root), so it gets --ignore-certificate-errors-spki-list with the SPKI hash of the wildcard LEAF (Chromium matches only
# certificates the server presents, and Traefik presents the leaf alone). The leaf is read from the cluster Secret and first
# verified against the local CA, so the pin is exactly "this certificate, signed by the local CA".
source "$HERE/../local-ca-lib.sh"
local_ca_detect "$ROOT"
ca=()
if [[ -n "$LOCAL_CA" ]]; then
  leaf="$("$ROOT/tk" kubectl -n traefik get secret apps-wildcard-tls -o jsonpath='{.data.tls\.crt}' | base64 -d)"
  openssl verify -x509_strict -CAfile "$LOCAL_CA" <(printf '%s\n' "$leaf") >/dev/null || { echo "  FAIL: the wildcard does not verify against $LOCAL_CA"; exit 1; }
  leaf_spki="$(printf '%s\n' "$leaf" | openssl x509 -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64)"
  ca=(-e "LEAF_SPKI=$leaf_spki" -e NODE_EXTRA_CA_CERTS=/local-ca.crt -v "$LOCAL_CA:/local-ca.crt:ro")
fi
run() { # run <command...>: inside the pinned image, as the host user, with the check directory mounted at /work
  local hosts=() h
  for h in "${HOSTS[@]}"; do hosts+=(--add-host "$h:$LB_IP"); done
  docker run --rm --init --user "$(id -u):$(id -g)" --network "$NET" "${hosts[@]}" "${ca[@]}" \
    --cap-drop ALL --security-opt no-new-privileges --shm-size=256m \
    -e HOME=/tmp -e npm_config_cache=/npmcache -e npm_config_update_notifier=false -e OUT_DIR=/work/out -e ORPHAN_HOST="$ORPHAN_HOST" \
    -v "$ROOT/.tools/npm/cache:/npmcache" -v "$HERE:/work:ro" -v "$HERE/node_modules:/work/node_modules" -v "$HERE/out:/work/out" -w /work "$IMG" "$@"
}

# Reinstall whenever package-lock.json changes (sha256 recorded next to the installed tree).
lock_sha="$(sha256sum "$HERE/package-lock.json" | cut -d' ' -f1)"
if [[ "$(cat "$HERE/node_modules/.lock-sha" 2>/dev/null || true)" != "$lock_sha" ]]; then
  echo "  (installing playwright from the lockfile; scripts disabled, browsers come from the image)"
  run npm ci --ignore-scripts --no-audit --no-fund >/dev/null
  echo "$lock_sha" > "$HERE/node_modules/.lock-sha"
fi
rm -f "$HERE"/out/*.png
# Fixture for the orphan-hosts scenario (cluster commands go through ./tk, which runs at the workspace root): the template
# smoke-orphan.yaml is rendered with this run's host and applied from stdin. Removed on exit by label, whatever the verdict (also
# leftovers of older runs); INT/TERM exit through the EXIT trap.
FIXTURE="$HERE/../smoke-orphan.yaml"
LABEL=atrium.wysted.dev/smoke=true
cd "$ROOT"
cleanup() {
  ./tk kubectl -n hello-a delete ingressroute -l "$LABEL" --ignore-not-found --wait=false >/dev/null || echo "WARN: orphan fixture not removed" >&2
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
template="$(<"$FIXTURE")"
printf '%s\n' "${template//__ORPHAN_HOST__/$ORPHAN_HOST}" | ./tk kubectl apply -f - >/dev/null
run node check.mjs
