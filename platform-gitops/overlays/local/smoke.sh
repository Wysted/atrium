#!/usr/bin/env bash
# End-to-end smoke of the platform in the k3d cluster, with curl from the HOST
# (*.apps.wysted.dev -> 127.0.0.1 through public DNS; Traefik serves the real wildcard certificate).
# Needs: up.sh already run, python3 on the host, ./gk (Docker) to build portalctl. Cluster access goes through ./tk.
# Leaves the cluster as found (temporary pods and fixtures are removed; alice's revoked grant is re-seeded).
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
cd "$ROOT"
K="./tk kubectl"
REL=platform-gitops/overlays/local
AUTH_HOST=auth.apps.wysted.dev
AUTH=https://$AUTH_HOST
A_HOST=hello-a.apps.wysted.dev
B_HOST=hello-b.apps.wysted.dev
A=https://$A_HOST
B=https://$B_HOST
P_HOST=portal.apps.wysted.dev
P=https://$P_HOST
HELLO_A_ID=app_01AFSXV5GZQ6FQ3BPFVTQWBBHV # seed.sql
PORTAL_APP_ID=app_01HQ3PD7RTA5M2XKWV9N8B4CYE # seed.sql, PORTAL_APP_ID and PLATFORM_PORTAL_APP_ID in the overlays
PROBE_DEADLINE_S=300 # the local portal probes every minute (PORTAL_PROBE_INTERVAL)
CURL_IMG=curlimages/curl:8.11.1
REVOKE_DEADLINE_MS=5000
LAST_USED_DEADLINE_S=150 # token last_used_at: throttled to once a minute per replica and flushed once a minute
WORK="$(mktemp -d)"
mkdir -p "$ROOT/.tools/tmp"
GW="$(mktemp -d -p "$ROOT/.tools/tmp")" # shared with the toolbox container (/workspace): Grafana checks and its credentials file
FAILS=0
FAILED=()
CURRENT_SECTION=""

pass() { echo "  PASS: $*"; }
fail() { echo "  FAIL: $*"; FAILS=$((FAILS + 1)); local m="$*"; m="${m//$'\n'/ }"; FAILED+=("[$CURRENT_SECTION] ${m:0:300}"); }
section() { CURRENT_SECTION="${1%%.*}"; echo; echo "== $*"; }
check() { if [[ "$2" == *"$3"* ]]; then pass "$1 ($3)"; else fail "$1: got '$2', want '$3'"; fi; }
check_not() { if [[ "$2" != *"$3"* ]]; then pass "$1"; else fail "$1: found '$3'"; fi; }
now_ms() { date +%s%3N; }

# req <jar> <curl args...>: headers in $WORK/h, body in $WORK/b; prints the status code.
req() { local jar="$1"; shift; curl -s -o "$WORK/b" -D "$WORK/h" -w '%{http_code}' -b "$jar" -c "$jar" "$@"; }
# req429 <jar> <curl args...>: like req, but waits out the /login rate limit (429 + Retry-After) a bounded number of times.
# In k3d Traefik sees every client as 10.42.0.1, so all logins of a run (and of back-to-back runs) share ONE bucket per
# platform-api replica (burst 30, 1 token / 2 s; see TODO.md "Real client IP"). The limit is never raised: the smoke
# waits for the bucket instead.
RATE_LIMIT_RETRIES=5
req429() {
  local code i
  for ((i = 0; i <= RATE_LIMIT_RETRIES; i++)); do
    code=$(req "$@")
    [[ "$code" == 429 ]] || { echo "$code"; return; }
    local secs; secs="$(header retry-after)"; secs="${secs:-2}"
    echo "  (rate limited, waiting ${secs}s)" >&2
    sleep "$secs"
  done
  echo "$code"
}
# header <name>: values of a response header from the last req.
header() { tr -d '\r' < "$WORK/h" | awk -v k="$(echo "$1" | tr A-Z a-z)" 'tolower($1)==k":" {sub(/^[^:]*: */,""); print}'; }
qparam() { python3 -c 'import sys,urllib.parse as u;print(u.parse_qs(u.urlparse(sys.argv[1]).query).get(sys.argv[2],[""])[0])' "$1" "$2"; }
nav() { req "$1" -H 'Accept: text/html' -H 'Sec-Fetch-Mode: navigate' "${@:2}"; }
api() { req "$1" -H 'Accept: application/json' -H 'Sec-Fetch-Mode: cors' "${@:2}"; }

# login <jar> <email> <app base url>: navigation -> login form -> fake login -> callback; leaves the app cookie in <jar>.
# Prints the final status. Stores the callback URL (with the one-time code) in $WORK/callback.
login() {
  local jar="$1" email="$2" app="$3" code loc rd bind
  code=$(nav "$jar" "$app/")
  [[ "$code" == 302 ]] || { echo "login: first hop $code"; return 1; }
  loc="$(header location)"
  rd="$(qparam "$loc" rd)"; bind="$(qparam "$loc" bind)"
  code=$(req429 "$jar" -H 'Accept: text/html' "$loc")
  if [[ "$code" == 200 ]]; then # login form: no central session yet
    code=$(req429 "$jar" -X POST -H "Origin: $AUTH" -H 'Sec-Fetch-Site: same-origin' \
      --data-urlencode "email=$email" --data-urlencode "rd=$rd" --data-urlencode "bind=$bind" "$AUTH/login")
  fi
  [[ "$code" == 302 ]] || { echo "login: auth host $code"; return 1; }
  echo "$(header location)" > "$WORK/callback"
  code=$(nav "$jar" "$(header location)")
  [[ "$code" == 302 ]] || { echo "login: callback $code"; return 1; }
  cp "$WORK/h" "$WORK/callback_h"
  nav "$jar" "$(header location)"
}

# Groups the portal section creates (SQL as the owner: the trap must not depend on a session). Audit rows stay (append-only).
drop_smoke_groups() {
  $K -n platform-db exec postgres-0 -- psql -q -U platform_owner -d platform \
    -c "DELETE FROM groups WHERE key IN ('smoke-group-ok', 'smoke-group-rejected')" >/dev/null 2>&1
}
# Grants section 23 creates (carol and the smoke robot, viewer on demo), as the owner like drop_smoke_groups.
drop_smoke_grants() {
  $K -n platform-db exec postgres-0 -- psql -q -U platform_owner -d platform \
    -c "DELETE FROM initiative_access a USING users u, initiative_roles r, initiatives i WHERE a.user_id = u.id AND r.id = a.role_id AND i.id = a.initiative_id AND i.key = 'demo' AND r.key = 'viewer' AND (u.email = 'carol@example.com' OR (u.kind = 'robot' AND u.name = 'smoke-robot'))" >/dev/null 2>&1
}
# Token cleanup as the owner (the trap must not depend on a session): revokes every live smoke token and disables the robot.
drop_smoke_tokens() {
  $K -n platform-db exec postgres-0 -- psql -q -U platform_owner -d platform \
    -c "UPDATE api_tokens SET revoked_at = now() WHERE name LIKE 'smoke-%' AND revoked_at IS NULL" \
    -c "UPDATE users SET status = 'disabled' WHERE kind = 'robot' AND name = 'smoke-robot'" >/dev/null 2>&1
}
# Section 22 disables the portal app for a moment (token denial check); the trap puts it back as the owner.
restore_portal_app() {
  $K -n platform-db exec postgres-0 -- psql -q -U platform_owner -d platform \
    -c "UPDATE apps SET status = 'active' WHERE id = '$PORTAL_APP_ID' AND status <> 'active'" >/dev/null 2>&1
}
cleanup() {
  restore_portal_app
  $K delete pod probe-default --ignore-not-found --wait=false >/dev/null 2>&1
  $K -n hello-a delete pod probe-hello --ignore-not-found --wait=false >/dev/null 2>&1
  $K -n traefik delete pod probe-traefik --ignore-not-found --wait=false >/dev/null 2>&1
  $K delete namespace smoke-app --ignore-not-found --wait=false >/dev/null 2>&1
  $K delete -f "$REL/smoke-whoami.yaml" -f "$REL/smoke-whoami-portal.yaml" --ignore-not-found --wait=false >/dev/null 2>&1
  drop_smoke_groups
  drop_smoke_tokens
  drop_smoke_grants
  $K -n monitoring delete pod probe-prom probe-grafana probe-other --ignore-not-found --wait=false >/dev/null 2>&1
  $K -n monitoring delete networkpolicy smoke-allow-egress --ignore-not-found >/dev/null 2>&1
  rm -rf "$WORK" "$GW"
}
trap cleanup EXIT
restore_portal_app # self-heal: a previous run may have died with the portal app disabled

echo "platform smoke: $(date -u +%FT%TZ)"
echo "  (creating long-lived probe pods first: kube-router needs a few seconds per new pod)"
$K delete pod probe-default --ignore-not-found >/dev/null 2>&1
$K run probe-default --image="$CURL_IMG" --restart=Never --command -- sleep 900 >/dev/null
$K -n hello-a delete pod probe-hello --ignore-not-found >/dev/null 2>&1
$K -n hello-a run probe-hello --image="$CURL_IMG" --restart=Never \
  --overrides='{"spec":{"automountServiceAccountToken":false,"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"probe-hello","image":"'"$CURL_IMG"'","command":["sleep","900"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' >/dev/null
# Temporary APP namespace without any NetworkPolicy: its pod can send anything, so what it cannot reach is blocked by the
# INGRESS policy of the destination (the app namespaces of the platform also have an egress policy that hides it).
$K delete namespace smoke-app --ignore-not-found --wait=true >/dev/null 2>&1
$K create namespace smoke-app >/dev/null
$K label namespace smoke-app portal.wysted.dev/app-namespace=true portal.wysted.dev/smoke=true >/dev/null
$K -n smoke-app run probe-open --image="$CURL_IMG" --restart=Never --command -- sleep 900 >/dev/null
$K wait --for=condition=Ready pod/probe-default --timeout=120s >/dev/null
$K -n smoke-app wait --for=condition=Ready pod/probe-open --timeout=120s >/dev/null
$K -n hello-a wait --for=condition=Ready pod/probe-hello --timeout=120s >/dev/null
# Stand-in for Traefik (the only source allowed to reach the internal listener 8081: /forwardauth, /readyz): a temporary pod
# carrying the label the ingress policy selects. The Traefik image has no curl. Its labels lack the chart's instance label,
# so the Traefik Service never selects it.
$K -n traefik delete pod probe-traefik --ignore-not-found >/dev/null 2>&1
# Accepted for the local smoke: while it runs, this pod also passes the app namespaces' ingress policies (Traefik's label).
$K -n traefik run probe-traefik --image="$CURL_IMG" --restart=Never --labels=app.kubernetes.io/name=traefik \
  --overrides='{"spec":{"automountServiceAccountToken":false,"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"probe-traefik","image":"'"$CURL_IMG"'","command":["sleep","900"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' >/dev/null
$K -n traefik wait --for=condition=Ready pod/probe-traefik --timeout=120s >/dev/null
# Pods in the monitoring namespace (restricted Pod Security) for section 19, created now for the same reason as the others.
# probe-prom and probe-grafana carry the label the real pods are selected by (and only the policies of that label apply);
# probe-other has neither and gets its own allow-all EGRESS policy so that the INGRESS policy of the target decides.
mon_pod() { # mon_pod <name> <labels>
  $K -n monitoring delete pod "$1" --ignore-not-found >/dev/null 2>&1
  $K -n monitoring run "$1" --image="$CURL_IMG" --restart=Never --labels="$2" \
    --overrides='{"spec":{"automountServiceAccountToken":false,"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"'"$1"'","image":"'"$CURL_IMG"'","command":["sleep","900"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' >/dev/null
}
$K -n monitoring apply -f - >/dev/null <<'YAML'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: smoke-allow-egress, namespace: monitoring}
spec:
  podSelector: {matchLabels: {smoke.wysted.dev/egress: all}}
  policyTypes: [Egress]
  egress: [{}]
YAML
mon_pod probe-prom app.kubernetes.io/name=prometheus
mon_pod probe-grafana app.kubernetes.io/name=grafana
mon_pod probe-other smoke.wysted.dev/egress=all
for p in probe-prom probe-grafana probe-other; do $K -n monitoring wait --for=condition=Ready pod/$p --timeout=120s >/dev/null; done

section "1. Unauthenticated: navigation -> 302 with bind cookie; API -> 401 problem+json"
J1="$WORK/j1"
code=$(nav "$J1" "$A/")
check "navigation status" "$code" 302
loc="$(header location)"
echo "  Location: $loc"
check "location is the auth host login" "$loc" "$AUTH/login?"
check "location carries rd" "$loc" "rd=https%3A%2F%2F$A_HOST%2F"
check "location carries bind" "$loc" "bind="
sc="$(header set-cookie)"
echo "  Set-Cookie: ${sc%%=*}=<redacted>; ${sc#*; }"
check "bind cookie" "$sc" "__Host-Http-platform_bind="
code=$(api "$WORK/j1b" "$A/api/whoami")
check "API status" "$code" 401
check "API content-type" "$(header content-type)" "application/problem+json"
echo "  body: $(cat "$WORK/b")"
check "login_url member" "$(cat "$WORK/b")" "login_url"

section "2. Full flow alice -> hello-a (cookie attributes, claims)"
JA="$WORK/alice"
code=$(login "$JA" alice@example.com "$A"); echo "  final navigation status: $code"
check "login flow ends in the app" "$code" 200
cb="$(cat "$WORK/callback")"
echo "  callback: ${cb%%=*}=<code>"
check "callback target" "$cb" "$A/_platform/callback?code="
sc="$(tr -d '\r' < "$WORK/callback_h" | awk 'tolower($1)=="set-cookie:"{sub(/^[^:]*: */,""); print}')"
echo "  callback Set-Cookie: $(echo "$sc" | sed -E 's/(platform_app=)[^;]+/\1<redacted>/')"
check "name" "$sc" "__Host-Http-platform_app="
for attr in "Path=/" "HttpOnly" "Secure" "SameSite=Lax"; do check "attr" "$sc" "$attr"; done
check_not "no Domain attribute" "$sc" "Domain="
check "Referrer-Policy" "$(tr -d '\r' < "$WORK/callback_h" | awk 'tolower($1)=="referrer-policy:"{print $2}')" "no-referrer"
code=$(api "$JA" "$A/api/whoami")
check "hello-a with cookie" "$code" 200
echo "  claims: $(cat "$WORK/b")"
check "email" "$(cat "$WORK/b")" alice@example.com
check "role viewer" "$(cat "$WORK/b")" viewer

section "3. SSO: alice (central session) enters hello-b with no login form"
code=$(nav "$JA" "$B/")
echo "  hello-b nav status: $code (302 = login hop with existing session)"
loc="$(header location)"
code=$(req "$JA" -H 'Accept: text/html' "$loc")
check "auth host redirects straight back with a code (no form)" "$code" 302
check "to hello-b callback" "$(header location)" "$B/_platform/callback?code="
code=$(nav "$JA" "$(header location)"); check "callback" "$code" 302
code=$(api "$JA" "$B/api/whoami")
check "hello-b with cookie (alice viewer on 'other')" "$code" 200
echo "  claims: $(cat "$WORK/b")"

section "4. carol (no grants) -> 403; bob (group demo-editors) -> hello-a 200 editor, hello-b 403"
JC="$WORK/carol"
login "$JC" carol@example.com "$A" >/dev/null
code=$(api "$JC" "$A/api/whoami"); check "carol hello-a API" "$code" 403; echo "  body: $(cat "$WORK/b")"
code=$(nav "$JC" "$A/"); check "carol hello-a navigation" "$code" 403
JB="$WORK/bob"
login "$JB" bob@example.com "$A" >/dev/null
code=$(api "$JB" "$A/api/whoami"); check "bob hello-a" "$code" 200
echo "  claims: $(cat "$WORK/b")"; check "role editor" "$(cat "$WORK/b")" editor
code=$(nav "$JB" "$B/"); loc="$(header location)"; req "$JB" -H 'Accept: text/html' "$loc" >/dev/null
nav "$JB" "$(header location)" >/dev/null
code=$(api "$JB" "$B/api/whoami"); check "bob hello-b (grant does not cross initiatives)" "$code" 403

section "5. Revocation propagates through NOTIFY (2 replicas) in < ${REVOKE_DEADLINE_MS} ms"
for _ in $(seq 1 12); do api "$JA" "$A/api/whoami" >/dev/null; done # warm the decision caches
code=$(api "$JA" "$A/api/whoami"); check "before revoke" "$code" 200
# Through Traefik only one replica may ever see the traffic (pooled keepalive connections), so the NOTIFY fan-out is
# also measured per replica: a Traefik-labelled probe pod calls /forwardauth on each pod IP while the grant is deleted.
IPS="$($K -n platform get pods -l app.kubernetes.io/name=platform-api -o jsonpath='{.items[*].status.podIP}')"
COOKIE="$(awk '$1 ~ /hello-a/ && $6=="__Host-Http-platform_app"{print $7}' "$JA")"
echo "  replicas: $IPS"
$K -n traefik exec probe-traefik -- sh -c 'end=$(( $(date +%s) + 10 )); while [ $(date +%s) -lt $end ]; do for ip in '"$IPS"'; do s=$(curl -s -o /dev/null --max-time 2 -w "%{http_code}" -H "X-Forwarded-Host: '$A_HOST'" -H "X-Forwarded-Method: GET" -H "X-Forwarded-Uri: /api/whoami" -H "Accept: application/json" -H "Sec-Fetch-Mode: cors" -H "Cookie: __Host-Http-platform_app='"$COOKIE"'" http://$ip:8081/forwardauth); echo "$(date +%s) $ip $s"; done; sleep 0.1; done' > "$WORK/replica.log" 2>/dev/null &
POLLER=$!
sleep 4 # both replicas answer 200 (and cache the decision) before the revoke
commit_ms="$($K -n platform-db exec postgres-0 -- psql -At -q -U platform_owner -d platform -c \
  "BEGIN; DELETE FROM initiative_access ia USING users u, initiatives i WHERE ia.user_id = u.id AND ia.initiative_id = i.id AND u.google_sub = 'fake:alice@example.com' AND i.key = 'demo'; SELECT (extract(epoch FROM clock_timestamp()) * 1000)::bigint; COMMIT;" | grep -E '^[0-9]{13}$')"
t0=$(now_ms); elapsed=-1
while (( $(now_ms) - t0 < 10000 )); do
  code=$(api "$JA" "$A/api/whoami")
  if [[ "$code" == 403 ]]; then elapsed=$(( $(now_ms) - t0 )); break; fi
  sleep 0.1
done
echo "  first 403 after ${elapsed} ms"
if (( elapsed >= 0 && elapsed < REVOKE_DEADLINE_MS )); then pass "revoked in ${elapsed} ms"; else fail "revocation took ${elapsed} ms"; fi
bad=0; for _ in $(seq 1 30); do [[ "$(api "$JA" "$A/api/whoami")" == 403 ]] || bad=$((bad + 1)); done
if (( bad == 0 )); then pass "30/30 following requests 403"; else fail "$bad of 30 following requests were not 403"; fi
code=$(api "$JA" "$B/api/whoami"); check "other grant untouched (hello-b)" "$code" 200
wait "$POLLER" 2>/dev/null
echo "  per-replica log (epoch-second resolution): $(wc -l < "$WORK/replica.log") samples, commit at ${commit_ms} ms"
for ip in $IPS; do
  before="$(awk -v ip="$ip" '$2==ip && $3==200' "$WORK/replica.log" | wc -l)"
  first="$(awk -v ip="$ip" '$2==ip && $3==403{print $1; exit}' "$WORK/replica.log")"
  flap="$(awk -v ip="$ip" '$2==ip && $3==403{f=1} f && $2==ip && $3!=403{c++} END{print c+0}' "$WORK/replica.log")"
  if [[ -z "$first" ]]; then fail "replica $ip never answered 403"; continue; fi
  delta=$(( first * 1000 - commit_ms ))
  echo "  replica $ip: $before x 200 before, first 403 at ${first}s (commit+${delta} ms, +-1000 ms), non-403 after first 403: $flap"
  if (( delta < REVOKE_DEADLINE_MS - 1000 && flap == 0 && before > 0 )); then pass "replica $ip revoked via NOTIFY"; else fail "replica $ip too slow or flapping"; fi
done
"$HERE/seed.sh" >/dev/null; sleep 1
code=$(api "$JA" "$A/api/whoami"); check "grant restored by seed" "$code" 200

section "6. Logout: GET 405, POST without Origin/Sec-Fetch-Site 403, POST with Origin revokes"
code=$(req "$JA" -H 'Sec-Fetch-Mode: navigate' -H 'Accept: text/html' "$A/_platform/logout"); check "GET" "$code" 405
code=$(req "$JA" -X POST "$A/_platform/logout"); check "POST without Origin/Sec-Fetch-Site" "$code" 403
code=$(req "$JA" -X POST -H "Origin: https://evil.example" -H 'Sec-Fetch-Site: cross-site' "$A/_platform/logout"); check "POST cross-site" "$code" 403
code=$(api "$JA" "$A/api/whoami"); check "still logged in after refused attempts" "$code" 200
code=$(req "$JA" -X POST -H "Origin: $A" "$A/_platform/logout"); check "POST with Origin" "$code" 303
check "to signed-out page" "$(header location)" "$AUTH/signed-out"
check "app cookie cleared" "$(header set-cookie)" "Max-Age=0"
code=$(nav "$JA" "$A/"); check "next navigation asks for login again" "$code" 302
check "to login" "$(header location)" "$AUTH/login?"
code=$(api "$JA" "$B/api/whoami"); check "hello-b cookie died with the central session" "$code" 401

section "7. Forged Platform-Assertion never reaches the backends (whoami fixture)"
$K apply -f "$REL/smoke-whoami.yaml" >/dev/null
$K -n hello-a rollout status deploy/smoke-whoami --timeout=120s >/dev/null
sleep 15 # kube-router needs a moment before a fresh pod is covered consistently
FORGED='Platform-Assertion: forged.jwt.value'
JA3="$WORK/alice3"; login "$JA3" alice@example.com "$A" >/dev/null
echo "  -- authorized request with forged header (app must see the platform-issued assertion, not the forged one):"
code=$(api "$JA3" -H "$FORGED" "$A/__smoke_whoami"); check "status" "$code" 200
body="$(cat "$WORK/b")"; check_not "forged value absent" "$body" "forged.jwt.value"; check "assertion present (issued by platform)" "$body" "Platform-Assertion: ey"
echo "  -- CORS preflight (ForwardAuth 200 without assertion):"
code=$(curl -s -o "$WORK/b" -w '%{http_code}' -X OPTIONS -H 'Origin: https://x.example' -H 'Access-Control-Request-Method: GET' -H "$FORGED" "$A/__smoke_whoami")
check "status" "$code" 200; check_not "no Platform-Assertion reaches the app" "$(cat "$WORK/b")" "Platform-Assertion"
echo "  -- auth host:"
code=$(curl -s -o "$WORK/b" -w '%{http_code}' -H "$FORGED" "$AUTH/__smoke_whoami")
check "status" "$code" 200; check_not "no Platform-Assertion reaches the backend" "$(cat "$WORK/b")" "Platform-Assertion"
check "request really got through (Host header echoed)" "$(cat "$WORK/b")" "Host: $AUTH_HOST"
$K delete -f "$REL/smoke-whoami.yaml" --wait=false >/dev/null

section "8. /forwardauth and the internal port are unreachable from outside"
for path in /forwardauth /readyz /healthz; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$AUTH$path"); check "$AUTH$path" "$code" 404
done
code=$(curl -s -o /dev/null -w '%{http_code}' -H "X-Forwarded-Host: $A_HOST" "$AUTH/forwardauth"); check "with spoofed X-Forwarded-Host" "$code" 404
check "jwks on the auth host is served (public listener)" "$(curl -s -o /dev/null -w '%{http_code}' "$AUTH/.well-known/jwks.json")" 200

section "9. NetworkPolicy (probe pods are long-lived: kube-router needs a few seconds per new pod)"
sleep 5
probe() { # probe <ns> <pod> <url>: HTTP status or 000
  $K -n "$1" exec "$2" -- curl -s -o /dev/null --max-time 4 -w '%{http_code}' "$3" 2>/dev/null || true
}
wait_probe() { # wait_probe <ns> <pod> <url> <want> [timeout-s]: poll until the status is <want> (positive controls only)
  local got="" end=$(( $(date +%s) + ${5:-30} ))
  while (( $(date +%s) < end )); do
    got="$(${WAIT_FN:-probe} "$1" "$2" "$3")"; [[ "$got" == "$4" ]] && break; sleep 1
  done
  echo "$got"
}
# JWKS listener speaks HTTPS only. The local CA is streamed into the pod's curl on stdin (no file in the pod, no mount).
JWKS_CA=.tools/secrets/k3d-jwks-ca.crt
JWKS_SVC=platform-api-jwks.platform.svc.cluster.local
JWKS_URL=https://$JWKS_SVC:8083/.well-known/jwks.json
probe_tls() { # probe_tls <ns> <pod> <url>: HTTP status or 000, server certificate verified against the local CA
  $K -n "$1" exec -i "$2" -- curl -s -o /dev/null --max-time 4 --cacert /dev/stdin -w '%{http_code}' "$3" < "$JWKS_CA" 2>/dev/null || true
}
probe_tlsk() { # like probe_tls, certificate NOT verified (pod-IP URLs, where the SAN cannot match): reachability only
  $K -n "$1" exec "$2" -- curl -sk -o /dev/null --max-time 4 -w '%{http_code}' "$3" 2>/dev/null || true
}
tls_rc() { # tls_rc <ns> <pod> <ca|noca> <curl args...>: curl exit code (0 ok, 60 verification failed, 35 handshake failed, 7 refused/blocked, 28 timeout)
  local ns="$1" pod="$2" mode="$3"; shift 3
  if [[ "$mode" == ca ]]; then
    $K -n "$ns" exec -i "$pod" -- curl -s -o /dev/null --max-time 4 --cacert /dev/stdin "$@" < "$JWKS_CA" >/dev/null 2>&1
  else
    $K -n "$ns" exec "$pod" -- curl -s -o /dev/null --max-time 4 "$@" >/dev/null 2>&1
  fi
  echo $?
}
wait_probe_tls() { WAIT_FN=probe_tls wait_probe "$@"; }
wait_probe_tlsk() { WAIT_FN=probe_tlsk wait_probe "$@"; }
probe_tcp() { # probe_tcp <ns> <pod> <host> <port>: connected / blocked / dns-error (curl 6: the name did not resolve)
  local rc
  rc="$($K -n "$1" exec "$2" -- sh -c "curl -s --max-time 4 -o /dev/null telnet://$3:$4 </dev/null; echo \$?" 2>/dev/null)"
  case "$rc" in 28) echo connected ;; 6) echo dns-error ;; *) echo blocked ;; esac
}
check "default -> platform-db:5432" "$(probe_tcp default probe-default postgres.platform-db.svc.cluster.local 5432)" blocked
check "default -> platform-api-internal:8081" "$(probe default probe-default http://platform-api-internal.platform.svc.cluster.local:8081/healthz)" 000
check "default -> platform-api-public:8080" "$(probe default probe-default http://platform-api-public.platform.svc.cluster.local:8080/login)" 000
check "default -> hello-a directly" "$(probe default probe-default http://hello.hello-a.svc.cluster.local:8080/healthz)" 000
check "hello-a pod -> platform-db:5432 (egress)" "$(probe_tcp hello-a probe-hello postgres.platform-db.svc.cluster.local 5432)" blocked
check "hello-a pod -> hello-b" "$(probe hello-a probe-hello http://hello.hello-b.svc.cluster.local:8080/healthz)" 000
check "hello-a pod -> platform-api-public:8080 (not allowed)" "$(probe hello-a probe-hello http://platform-api-public.platform.svc.cluster.local:8080/login)" 000
echo "  -- positive controls:"
check "hello-a pod -> JWKS on platform-api-jwks:8083" "$(probe_tls hello-a probe-hello $JWKS_URL)" 200
check "Traefik-labelled pod -> platform-api-internal /healthz (8081)" "$(probe traefik probe-traefik http://platform-api-internal.platform.svc.cluster.local:8081/healthz)" 200
check "platform-api reaches PostgreSQL (readyz via internal listener, from Traefik)" "$(probe traefik probe-traefik http://platform-api-internal.platform.svc.cluster.local:8081/readyz)" 200
check "internal listener does not serve the JWKS (404)" "$(probe traefik probe-traefik http://platform-api-internal.platform.svc.cluster.local:8081/.well-known/jwks.json)" 404
check "hello-a pod -> platform-api-internal:8081 /healthz (app namespaces cannot reach it)" "$(probe hello-a probe-hello http://platform-api-internal.platform.svc.cluster.local:8081/healthz)" 000
check "Traefik-labelled pod -> platform-api-jwks:8083 (Traefik does not need it)" "$(probe_tls traefik probe-traefik $JWKS_URL)" 000

section "10. Pods: no ServiceAccount token, DB role, KEK mode"
for target in "platform deploy/platform-api" "portal deploy/portal" "hello-a deploy/hello" "hello-b deploy/hello" "platform-db statefulset/postgres"; do
  set -- $target
  tmpl=".spec.template.spec"
  auto="$($K -n "$1" get "$2" -o jsonpath="{$tmpl.automountServiceAccountToken}")"
  vols="$($K -n "$1" get "$2" -o jsonpath="{$tmpl.volumes[*].name}")"
  echo "  $1/$2: automountServiceAccountToken=$auto volumes=[$vols]"
  check "$1/$2 automount" "$auto" false
  check_not "$1/$2 no projected token volume" "$vols" kube-api-access
done
pod="$($K -n platform get pod -l app.kubernetes.io/name=platform-api -o jsonpath='{.items[0].metadata.name}')"
echo "  pod $pod mounts: $($K -n platform get pod "$pod" -o jsonpath='{range .spec.volumes[*]}{.name}{" "}{end}')"
check "pod has no kube-api-access volume" "$($K -n platform get pod "$pod" -o jsonpath='{.spec.volumes[*].name}')" "kek pgpass"
check_not "no token volume" "$($K -n platform get pod "$pod" -o jsonpath='{.spec.volumes[*].name}')" kube-api-access
echo "  kek volume defaultMode (octal 0440 = 288): $($K -n platform get pod "$pod" -o jsonpath='{.spec.volumes[?(@.name=="kek")].secret.defaultMode}'); fsGroup=$($K -n platform get pod "$pod" -o jsonpath='{.spec.securityContext.fsGroup}')"
check "KEK mode 0440 accepted (pod Ready)" "$($K -n platform get pod "$pod" -o jsonpath='{.status.containerStatuses[0].ready}')" true
line="$($K -n platform logs "$pod" 2>/dev/null | grep 'database role verified' | head -1)"
echo "  $line"
check "database role verified" "$line" '"current_user":"platform_runtime"'
check "login role" "$line" '"session_user":"platform_api"'
# Least privilege (migration 0010): statements run as platform_runtime inside a transaction that is always rolled back.
as_runtime() { # as_runtime <sql>: psql output (errors included) of one statement under SET ROLE platform_runtime (bootstrap superuser: owner cannot SET ROLE)
  printf 'BEGIN;\nSET LOCAL ROLE platform_runtime;\n%s;\nROLLBACK;\n' "$1" \
    | $K -n platform-db exec -i postgres-0 -- psql -q -U postgres_admin -d platform 2>&1
}
check "runtime cannot INSERT into initiatives" "$(as_runtime 'INSERT INTO initiatives DEFAULT VALUES')" "permission denied for table initiatives"
check "runtime cannot UPDATE users.status" "$(as_runtime 'UPDATE users SET status = status')" "permission denied for table users"
out="$(as_runtime "UPDATE deployments SET last_seen_at = last_seen_at RETURNING 'runtime-update-ok'")"
check_not "runtime can UPDATE deployments.last_seen_at" "$out" "permission denied"
check_not "runtime UPDATE deployments.last_seen_at has no error" "$out" "ERROR"
check "runtime UPDATE deployments.last_seen_at returned rows" "$out" "runtime-update-ok"

section "11. Traefik access log: no code, no cookie values; client IP seen"
CODE="$(qparam "$(cat "$WORK/callback")" code)"
JW="$WORK/alice4"; login "$JW" alice@example.com "$A" >/dev/null
CODE2="$(qparam "$(cat "$WORK/callback")" code)"
APPCOOKIE="$(awk '$6=="__Host-Http-platform_app"{print $7}' "$JW")"
SESSCOOKIE="$(awk '$6=="__Host-Http-platform_session"{print $7}' "$JW")"
$K -n traefik logs deploy/traefik --since=15m > "$WORK/access.log" 2>/dev/null
echo "  access log lines scanned: $(wc -l < "$WORK/access.log")"
echo "  callback requests logged as: $(grep '_platform/callback' "$WORK/access.log" | tail -1 | sed -E 's/^([^ ]+) .*(\"GET [^\"]*\").*/\1 \2/')"
for v in "$CODE" "$CODE2"; do [[ -n "$v" ]] && { grep -qF "$v" "$WORK/access.log" && fail "login code found in access log" || pass "login code not in access log"; }; done
[[ -n "$APPCOOKIE" ]] && { grep -qF "$APPCOOKIE" "$WORK/access.log" && fail "app cookie value in access log" || pass "app cookie value not in access log"; }
[[ -n "$SESSCOOKIE" ]] && { grep -qF "$SESSCOOKIE" "$WORK/access.log" && fail "session cookie value in access log" || pass "session cookie value not in access log"; }
check_not "no 'code=' in the log" "$(cat "$WORK/access.log")" "code="
check_not "no 'bind=' / 'rd=' in the log" "$(cat "$WORK/access.log")" "bind="
echo "  client address Traefik sees (first field of recent requests): $(grep 'hello-a\|hello-b\|platform' "$WORK/access.log" | awk '{print $1}' | sort | uniq -c | sort -rn | head -3 | tr '\n' ';')"

section "12. Killing a platform-api replica mid-flow does not break the flow"
JK="$WORK/alice5"; login "$JK" alice@example.com "$A" >/dev/null
victim="$($K -n platform get pod -l app.kubernetes.io/name=platform-api -o jsonpath='{.items[0].metadata.name}')"
( sleep 3; $K -n platform delete pod "$victim" --wait=false >/dev/null ) &
declare -A seen
for i in $(seq 1 80); do
  c=$(api "$JK" "$A/api/whoami"); seen[$c]=$(( ${seen[$c]:-0} + 1 )); sleep 0.15
done
wait
for c in "${!seen[@]}"; do echo "  status $c: ${seen[$c]}"; done
if [[ "${#seen[@]}" == 1 && -n "${seen[200]:-}" ]]; then pass "80/80 requests 200 while $victim was deleted"; else fail "non-200 responses while a replica was killed"; fi
$K -n platform rollout status deploy/platform-api --timeout=120s >/dev/null
JN="$WORK/dave"; code=$(login "$JN" bob@example.com "$A"); check "a fresh login flow still works afterwards" "$code" 200

# --- portal (contract block 6) --------------------------------------------------------------------------------------
# json_get <file> <python expression over d>: value from a JSON body.
json_get() { python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$1" "$2" 2>/dev/null; }
# mutate <jar> <method> <url> <sec-fetch-site|-> <content-type|-> [json body]: status of a portal mutation.
mutate() {
  local jar="$1" method="$2" url="$3" site="$4" ctype="$5" body="${6:-}" args=()
  [[ "$site" != - ]] && args+=(-H "Sec-Fetch-Site: $site")
  [[ "$ctype" != - ]] && args+=(-H "Content-Type: $ctype")
  [[ -n "$body" ]] && args+=(-d "$body")
  api "$jar" -X "$method" -H "Origin: $P" "${args[@]}" "$url"
}

# Section 12 deletes a platform-api replica: a probe round that overlaps it is legitimately inconclusive (control sample
# fails, WARN). Findings are judged from here on; the secrets-in-logs checks still scan the whole log.
SINCE_13="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

section "13. Portal home through Traefik with the fake login"
JP="$WORK/portal-alice"
code=$(nav "$JP" "$P/"); check "portal navigation without session" "$code" 302
check "to the auth host login" "$(header location)" "$AUTH/login?"
check "carries rd for the portal host" "$(header location)" "rd=https%3A%2F%2F$P_HOST%2F"
code=$(api "$WORK/portal-anon" "$P/api/v1/me"); check "API without session" "$code" 401
login "$JP" alice@example.com "$P" >/dev/null
code=$(api "$JP" "$P/api/v1/me"); check "GET /api/v1/me" "$code" 200
echo "  body: $(cat "$WORK/b")"; check "alice profile" "$(cat "$WORK/b")" alice@example.com
check "alice is not a platform admin" "$(json_get "$WORK/b" 'd["permissions"]["platform_admin"]')" False
code=$(api "$JP" "$P/api/v1/me/apps"); check "GET /api/v1/me/apps" "$code" 200
apps="$(cat "$WORK/b")"; echo "  hosts: $(json_get "$WORK/b" '[h["host"] if "host" in h else h for h in d["apps"]]')"
check "lists hello-a" "$apps" "$A_HOST"; check "lists hello-b" "$apps" "$B_HOST"
check_not "does not list the portal itself" "$apps" "$P_HOST"
code=$(api "$JP" "$P/api/v1/groups"); check "alice (not admin) cannot list groups" "$code" 403

section "14. Admin mutations: same-origin JSON accepted; same-site/cross-site 403; text/plain 415"
JAD="$WORK/portal-admin"
login "$JAD" admin@example.com "$P" >/dev/null
code=$(api "$JAD" "$P/api/v1/me"); check "admin GET /api/v1/me" "$code" 200
check "admin is a platform admin" "$(json_get "$WORK/b" 'd["permissions"]["platform_admin"]')" True
# Re-runnable: remove a leftover of an interrupted run through the API first (the trap also removes it by SQL).
code=$(api "$JAD" "$P/api/v1/groups")
for old in $(json_get "$WORK/b" '" ".join(g["id"] for g in d.get("groups", d.get("items", [])) if g["key"].startswith("smoke-group-"))'); do
  code=$(mutate "$JAD" DELETE "$P/api/v1/groups/$old" same-origin application/json); echo "  removed leftover group $old ($code)"
done
code=$(mutate "$JAD" POST "$P/api/v1/groups" same-origin application/json '{"key":"smoke-group-ok","name":"Smoke"}'); check "POST same-origin + JSON" "$code" 201
gid="$(json_get "$WORK/b" 'd["id"]')"; echo "  created group $gid"
check_not "same-origin response has no Access-Control-*" "$(tr -d '\r' < "$WORK/h" | tr A-Z a-z)" "access-control-"
BAD='{"key":"smoke-group-rejected","name":"Smoke"}'
code=$(mutate "$JAD" POST "$P/api/v1/groups" same-site application/json "$BAD"); check "POST Sec-Fetch-Site same-site" "$code" 403
code=$(mutate "$JAD" POST "$P/api/v1/groups" cross-site application/json "$BAD"); check "POST Sec-Fetch-Site cross-site" "$code" 403
code=$(mutate "$JAD" POST "$P/api/v1/groups" same-origin text/plain "$BAD"); check "POST same-origin + text/plain" "$code" 415
code=$(mutate "$JAD" POST "$P/api/v1/groups" same-origin application/x-www-form-urlencoded "key=smoke-group-rejected"); check "POST same-origin + form" "$code" 415
code=$(api "$JAD" -X POST -H "Origin: $A" -H 'Content-Type: application/json' -d "$BAD" "$P/api/v1/groups"); check "POST sibling Origin, no Sec-Fetch-Site" "$code" 403
code=$(mutate "$JAD" DELETE "$P/api/v1/groups/$gid" same-site application/json); check "DELETE same-site" "$code" 403
code=$(mutate "$JAD" DELETE "$P/api/v1/groups/$gid" same-origin -); check "DELETE without Content-Type" "$code" 415
code=$(api "$JAD" -H 'Sec-Fetch-Site: same-site' "$P/api/v1/groups"); check "GET /api/* with same-site (read isolation)" "$code" 403
code=$(api "$JAD" "$P/api/v1/groups"); check "admin lists groups" "$code" 200
check_not "no rejected mutation was applied" "$(cat "$WORK/b")" smoke-group-rejected
check "the accepted one was" "$(cat "$WORK/b")" smoke-group-ok
code=$(mutate "$JAD" DELETE "$P/api/v1/groups/$gid" same-origin application/json); check "DELETE same-origin + JSON (cleanup)" "$code" 204

section "14b. Initiative manager (dave, manages_access on demo only): scoped list, read-only admin API"
JDV="$WORK/portal-dave"
login "$JDV" dave@example.com "$P" >/dev/null
code=$(api "$JDV" "$P/api/v1/me"); check "dave GET /api/v1/me" "$code" 200
check "dave is not a platform admin" "$(json_get "$WORK/b" 'd["permissions"]["platform_admin"]')" False
code=$(api "$JDV" "$P/api/v1/initiatives"); check "dave lists initiatives" "$code" 200
check "dave sees exactly demo" "$(json_get "$WORK/b" '[i["key"] for i in d["initiatives"]]')" "['demo']"
DEMO_ID="$(json_get "$WORK/b" 'd["initiatives"][0]["id"]')"
code=$(api "$JAD" "$P/api/v1/initiatives"); OTHER_ID="$(json_get "$WORK/b" '[i["id"] for i in d["initiatives"] if i["key"] == "other"][0]')"
PLATFORM_ID="$(json_get "$WORK/b" '[i["id"] for i in d["initiatives"] if i["key"] == "platform"][0]')"
code=$(api "$JAD" "$P/api/v1/initiatives/$PLATFORM_ID/apps"); check "admin lists the apps of the platform initiative" "$code" 200
check "  (the portal app carries portal_app true)" "$(json_get "$WORK/b" '[a["portal_app"] for a in d["apps"] if a["id"] == "'"$PORTAL_APP_ID"'"][0]')" True
code=$(api "$JAD" "$P/api/v1/initiatives/$DEMO_ID/apps"); check "admin lists the apps of demo" "$code" 200
check "  (hello-a carries portal_app false)" "$(json_get "$WORK/b" '[a["portal_app"] for a in d["apps"] if a["id"] == "'"$HELLO_A_ID"'"][0]')" False
code=$(api "$JDV" "$P/api/v1/initiatives/$DEMO_ID"); check "dave GET demo" "$code" 200
code=$(api "$JDV" "$P/api/v1/initiatives/$OTHER_ID"); check "dave GET other initiative (out of scope)" "$code" 404
code=$(mutate "$JDV" PATCH "$P/api/v1/initiatives/$DEMO_ID" same-origin application/json '{"name":"Demo (manager)"}'); check "dave PATCH demo name" "$code" 403
code=$(mutate "$JDV" POST "$P/api/v1/initiatives" same-origin application/json '{"key":"smoke-dave","name":"Smoke"}'); check "dave POST initiative" "$code" 403
code=$(api "$JDV" "$P/api/v1/me/apps"); apps="$(cat "$WORK/b")"
check "dave's home lists hello-a" "$apps" "$A_HOST"; check_not "dave's home does not list hello-b (no grants in other)" "$apps" "$B_HOST"
code=$(api "$JDV" "$P/api/v1/groups"); check "dave (manager) may list groups, to grant access (portal.ListGroups)" "$code" 200
code=$(api "$JAD" "$P/api/v1/groups?q=demo-editors"); check "admin GET /groups?q= filters by key" "$code" 200
check "  (q=demo-editors returns only demo-editors)" "$(json_get "$WORK/b" '[g["key"] for g in d["groups"]]')" "['demo-editors']"
DE_ID="$(json_get "$WORK/b" 'd["groups"][0]["id"]')"
code=$(api "$JDV" "$P/api/v1/groups?q=DEMO"); check "dave GET /groups?q= (case-insensitive name match)" "$code" 200
check "  (q=DEMO finds demo-editors)" "$(cat "$WORK/b")" demo-editors
code=$(api "$JAD" "$P/api/v1/groups?q=zzz-no-such-group"); check "GET /groups?q= with no match" "$code" 200
check "  (empty list)" "$(json_get "$WORK/b" 'len(d["groups"])')" 0
code=$(api "$JAD" "$P/api/v1/groups?q=%25"); check "GET /groups?q=%25 (literal percent)" "$code" 200
check "  (no group matches a literal %)" "$(json_get "$WORK/b" 'len(d["groups"])')" 0
code=$(api "$JAD" "$P/api/v1/groups?q=$(printf 'a%.0s' $(seq 1 64))"); check "GET /groups?q= of 64 characters" "$code" 200
code=$(api "$JAD" "$P/api/v1/groups?q=$(printf 'a%.0s' $(seq 1 65))"); check "GET /groups?q= of 65 characters" "$code" 422
code=$(api "$JAD" "$P/api/v1/groups/$DE_ID"); check "admin GET /groups/{id}" "$code" 200
check "  (key demo-editors)" "$(json_get "$WORK/b" 'd["key"]')" demo-editors
code=$(api "$JDV" "$P/api/v1/groups/$DE_ID"); check "dave (manager) GET /groups/{id}" "$code" 200
code=$(api "$JP" "$P/api/v1/groups/$DE_ID"); check "alice (not admin) GET /groups/{id}" "$code" 403
code=$(api "$JAD" "$P/api/v1/groups/00000000-0000-4000-8000-000000000000"); check "admin GET /groups/{unknown id}" "$code" 404

section "15. Portal responses: security headers, no CORS, /metrics, probes and /_platform/ not served by the portal"
code=$(api "$JAD" -H 'Origin: https://evil.example' "$P/api/v1/me"); check "GET with a foreign Origin" "$code" 200
hdrs="$(tr -d '\r' < "$WORK/h" | tr A-Z a-z)"
for h in "content-security-policy: default-src 'none'; frame-ancestors 'none'" "strict-transport-security: max-age=63072000; includesubdomains" \
         "x-frame-options: deny" "cross-origin-opener-policy: same-origin" "cross-origin-resource-policy: same-origin" \
         "referrer-policy: no-referrer" "x-content-type-options: nosniff"; do check "header" "$hdrs" "$h"; done
check_not "no Access-Control-* on the response" "$hdrs" "access-control-"
code=$(curl -s -o "$WORK/b" -D "$WORK/h" -w '%{http_code}' -X OPTIONS -H "Origin: https://evil.example" -H 'Access-Control-Request-Method: POST' "$P/api/v1/groups")
echo "  preflight status: $code"; check_not "no Access-Control-* on a preflight" "$(tr -d '\r' < "$WORK/h" | tr A-Z a-z)" "access-control-"
# Contract: /metrics lives on the metrics listener only and the probes on the internal one; the web handler (SPA fallback)
# must never answer them on the public host. With a session they are a JSON 404, never the SPA shell. The /_platform/
# prefix is reserved on every app host: ForwardAuth only intercepts the callback (with a code) and logout; the rest of
# the subtree reaches the portal, which answers the same JSON 404.
for path in /metrics /metrics/ /healthz /readyz /_platform/nope; do
  code=$(api "$JAD" "$P$path"); check "authenticated $path on the public host" "$code" 404
  check "  (problem+json)" "$(header content-type)" "application/problem+json"
  check_not "  (not the SPA shell)" "$(cat "$WORK/b")" "<html"
  check_not "  (no Prometheus exposition / probe answer)" "$(cat "$WORK/b")" "# HELP"
done
code=$(nav "$JAD" "$P/_platform/nope"); check "authenticated navigation to /_platform/nope" "$code" 404
check "  (problem+json, never text/html)" "$(header content-type)" "application/problem+json"
code=$(curl -s -o "$WORK/b" -w '%{http_code}' -H 'Accept: application/json' "$P/metrics"); check "anonymous /metrics on the public host (ForwardAuth, API request)" "$code" 401
code=$(curl -s -o "$WORK/b" -w '%{http_code}' -H 'Accept: application/json' "$P/_platform/nope"); check "anonymous /_platform/nope (ForwardAuth, API request)" "$code" 401

section "16. Metrics and internal ports are closed to app namespaces; JWKS stays open"
# Negatives come only from smoke-app: an app-labelled namespace WITHOUT policies of its own, so the destination's ingress
# policy alone decides (hello-a also has an egress policy that would hide an ingress regression). hello-a keeps the JWKS
# positive. Every negative has a positive first: the listener is up (apiserver pod proxy, which comes from the node and is
# never filtered) and a pod in the same position reaches what it may.
PA_PODS="$($K -n platform get pods -l app.kubernetes.io/name=platform-api -o jsonpath='{.items[*].metadata.name}')"
PO_PODS="$($K -n portal get pods -l app.kubernetes.io/name=portal -o jsonpath='{.items[*].metadata.name}')"
pod_ip() { $K -n "$1" get pod "$2" -o jsonpath='{.status.podIP}'; }
metrics_up() { # metrics_up <ns> <pod>: the metrics listener serves go_goroutines (through the apiserver proxy)
  $K get --raw "/api/v1/namespaces/$1/pods/$2:9090/proxy/metrics" 2>/dev/null | grep -c '^go_goroutines ' || true
}
echo "  platform-api pods: $PA_PODS; portal pods: $PO_PODS"
sleep 5
check "hello-a: JWKS on platform-api-jwks:8083 (positive control)" "$(wait_probe_tls hello-a probe-hello $JWKS_URL 200)" 200
check "smoke-app: JWKS on platform-api-jwks:8083 (positive control)" "$(wait_probe_tls smoke-app probe-open $JWKS_URL 200)" 200
check "smoke-app -> platform-api-internal:8081 /healthz (app namespaces cannot reach the internal listener)" "$(probe smoke-app probe-open http://platform-api-internal.platform.svc.cluster.local:8081/healthz)" 000
check "smoke-app -> platform-api-internal:8081 /forwardauth" "$(probe smoke-app probe-open http://platform-api-internal.platform.svc.cluster.local:8081/forwardauth)" 000
for pod in $PA_PODS; do
  ip="$(pod_ip platform "$pod")"
  # The replacement replica of section 12 may not be in kube-router's allow set yet: wait until its JWKS answers.
  check "smoke-app: platform-api $pod :8083 JWKS by pod IP (positive control)" "$(wait_probe_tlsk smoke-app probe-open https://$ip:8083/.well-known/jwks.json 200)" 200
  check "smoke-app: platform-api $pod :8083 serves nothing but the JWKS (/healthz is 404)" "$(probe_tlsk smoke-app probe-open https://$ip:8083/healthz)" 404
  check "Traefik-labelled pod -> platform-api $pod :8081 /healthz (positive control)" "$(wait_probe traefik probe-traefik http://$ip:8081/healthz 200)" 200
  check "smoke-app -> platform-api $pod $ip:8081 (internal listener, Traefik only)" "$(probe smoke-app probe-open http://$ip:8081/healthz)" 000
  check "platform-api $pod: metrics listener is up (apiserver proxy)" "$(metrics_up platform "$pod")" 1
  check "smoke-app -> platform-api $pod $ip:9090/metrics" "$(probe smoke-app probe-open http://$ip:9090/metrics)" 000
  check "smoke-app -> platform-api $pod $ip:8080 (public listener, Traefik only)" "$(probe smoke-app probe-open http://$ip:8080/login)" 000
done
for pod in $PO_PODS; do
  ip="$(pod_ip portal "$pod")"
  check "portal $pod: metrics listener is up (apiserver proxy)" "$(metrics_up portal "$pod")" 1
  check "smoke-app -> portal $pod $ip:9090/metrics" "$(probe smoke-app probe-open http://$ip:9090/metrics)" 000
  check "smoke-app -> portal $pod $ip:8080 (public listener, Traefik only)" "$(probe smoke-app probe-open http://$ip:8080/api/v1/me)" 000
  check "smoke-app -> portal $pod $ip:8081 (internal listener, kubelet only)" "$(probe smoke-app probe-open http://$ip:8081/healthz)" 000
  check "default ns -> portal $pod $ip:8080" "$(probe default probe-default http://$ip:8080/api/v1/me)" 000
done
# The postgres policy now also admits the portal namespace: an app namespace must still not reach the database.
check "postgres is up (pg_isready in the pod)" "$($K -n platform-db exec postgres-0 -- pg_isready -h 127.0.0.1 -p 5432 2>&1)" "accepting connections"
check "platform-api reaches PostgreSQL (readyz, positive control)" "$(probe traefik probe-traefik http://platform-api-internal.platform.svc.cluster.local:8081/readyz)" 200
check "probe_tcp sees an open port (positive control: platform-api-jwks:8083)" "$(probe_tcp smoke-app probe-open platform-api-jwks.platform.svc.cluster.local 8083)" connected
check "probe_tcp: smoke-app -> platform-api-internal:8081 is blocked" "$(probe_tcp smoke-app probe-open platform-api-internal.platform.svc.cluster.local 8081)" blocked
check "smoke-app (app namespace) -> postgres:5432" "$(probe_tcp smoke-app probe-open postgres.platform-db.svc.cluster.local 5432)" blocked

section "16b. JWKS internal TLS: HTTPS only, SAN, CA verification, TLS 1.3 (local CA from jwks-tls.sh)"
# Every negative has its positive in the same position: same pod, same URL, only the variable under test changes.
SAN_WANT="platform-api-jwks platform-api-jwks.platform platform-api-jwks.platform.svc platform-api-jwks.platform.svc.cluster.local"
# Exact comparison: one SAN per line, whole-line match (check is substring-based).
sans="$($K -n platform get secret platform-api-jwks-tls -o jsonpath='{.data.tls\.crt}' \
  | ./tk sh -c 'base64 -d | openssl x509 -noout -ext subjectAltName' 2>&1 | tail -n +2 | tr ',' '\n' | sed 's/^ *//')"
for n in $SAN_WANT; do
  if grep -Fxq "DNS:$n" <<<"$sans"; then pass "Secret certificate SAN (DNS:$n)"; else fail "Secret certificate SAN: DNS:$n missing"; fi
done
check "Secret certificate SAN: exact count of DNS entries" "$(grep -c '^DNS:' <<<"$sans")" 4
check "Secret certificate SAN: no other entry types" "$(grep -vc '^DNS:' <<<"$sans" || true)" 0
check "Secret platform-api-jwks-tls is kubernetes.io/tls" "$($K -n platform get secret platform-api-jwks-tls -o jsonpath='{.type}')" kubernetes.io/tls
skeys="$($K -n platform get secret platform-api-jwks-tls -o jsonpath='{.data}' | ./tk jq -r 'keys|join(",")')"
check "Secret keys are exactly tls.crt,tls.key" "$skeys" "tls.crt,tls.key"
check_not "Secret has no ca.key" "$skeys" "ca.key"
check_not "Secret has no ca.crt (leaf only)" "$skeys" "ca.crt"
for ns in hello-a hello-b portal; do
  ckeys="$($K -n $ns get configmap platform-jwks-ca -o jsonpath='{.data}' | ./tk jq -r 'keys|join(",")')"
  [[ $ckeys == ca.crt ]] && pass "ConfigMap platform-jwks-ca in $ns keys are exactly ca.crt" || fail "ConfigMap platform-jwks-ca in $ns: keys '$ckeys', want exactly ca.crt"
  check_not "ConfigMap platform-jwks-ca in $ns has no ca.key" "$ckeys" "ca.key"
done
check "leaf verifies against the CA for the service name (openssl verify)" \
  "$(./tk sh -c 'openssl verify -CAfile .tools/secrets/k3d-jwks-ca.crt -verify_hostname platform-api-jwks.platform.svc.cluster.local .tools/secrets/k3d-jwks-tls.crt' 2>&1)" "OK"
check "leaf does NOT verify for another name (negative control of the SAN check)" \
  "$(./tk sh -c 'openssl verify -CAfile .tools/secrets/k3d-jwks-ca.crt -verify_hostname platform-api-internal.platform.svc.cluster.local .tools/secrets/k3d-jwks-tls.crt' 2>&1)" "hostname mismatch"
echo "  -- from hello-a (app namespace):"
check "positive: HTTPS + CA -> 200" "$(probe_tls hello-a probe-hello $JWKS_URL)" 200
body="$($K -n hello-a exec -i probe-hello -- curl -s --max-time 4 --cacert /dev/stdin "$JWKS_URL" < "$JWKS_CA" 2>/dev/null)"
check "positive: body is a JWKS" "$body" '"keys"'
check "positive: curl exit code with the CA" "$(tls_rc hello-a probe-hello ca "$JWKS_URL")" 0
check "no CA (system roots only) -> verification fails (curl 60)" "$(tls_rc hello-a probe-hello noca "$JWKS_URL")" 60
check "no CA, certificate check off -> reachable (the failure above is verification, not the network)" "$(tls_rc hello-a probe-hello noca -k "$JWKS_URL")" 0
pa_ip="$(pod_ip platform "$(echo $PA_PODS | awk '{print $1}')")"
check "CA + pod-IP URL -> SAN mismatch (curl 60)" "$(tls_rc hello-a probe-hello ca "https://$pa_ip:8083/.well-known/jwks.json")" 60
check "CA + pod-IP URL with --resolve to the service name -> verified (positive control)" \
  "$(tls_rc hello-a probe-hello ca --resolve "$JWKS_SVC:8083:$pa_ip" "$JWKS_URL")" 0
check "plain HTTP on :8083 is not served (TLS only)" "$($K -n hello-a exec probe-hello -- curl -s -o /dev/null --max-time 4 -w '%{http_code}' "http://$JWKS_SVC:8083/.well-known/jwks.json" 2>/dev/null | grep -c '^200$' || true)" 0
check "plain HTTP: the port is open (TCP connect, positive control)" "$(probe_tcp hello-a probe-hello $JWKS_SVC 8083)" connected
check "TLS 1.3 accepted" "$(tls_rc hello-a probe-hello ca --tlsv1.3 "$JWKS_URL")" 0
check "TLS 1.2 refused (handshake failure, curl 35)" "$(tls_rc hello-a probe-hello ca --tls-max 1.2 "$JWKS_URL")" 35
echo "  -- rotation (not automated here): re-run jwks-tls.sh after deleting .tools/secrets/k3d-jwks-tls.* (new leaf, same CA),"
echo "     wait ~1-2 min (kubelet volume sync + 30 s reload); apps keep verifying, no restart, then compare the served serial."

section "17. Integration probe: hello-a health shows an ok probe and a seen host (within ${PROBE_DEADLINE_S}s)"
JADH="$WORK/portal-admin-health"; login "$JADH" admin@example.com "$P" >/dev/null
# Traffic on hello-a so last_seen_at is set whatever the flusher interval is.
JAS="$WORK/alice-seen"; login "$JAS" alice@example.com "$A" >/dev/null
for _ in 1 2 3; do api "$JAS" "$A/api/whoami" >/dev/null; done
t0=$(date +%s); status=""; seen=""; fresh=""
while (( $(date +%s) - t0 < PROBE_DEADLINE_S )); do
  code=$(api "$JADH" "$P/api/v1/apps/$HELLO_A_ID/health")
  if [[ "$code" == 200 ]]; then
    status="$(json_get "$WORK/b" 'd["app"]["deployments"][0]["last_probe_status"]')"
    seen="$(json_get "$WORK/b" 'd["app"]["deployments"][0]["last_seen_at"]')"
    fresh="$(json_get "$WORK/b" '__import__("datetime").datetime.now(__import__("datetime").timezone.utc) - __import__("datetime").datetime.fromisoformat(d["app"]["deployments"][0]["last_probe_at"].replace("Z","+00:00")) < __import__("datetime").timedelta(minutes=3)')"
    [[ "$status" == ok && -n "$seen" && "$seen" != None && "$fresh" == True ]] && break
  fi
  sleep 5
done
echo "  after $(( $(date +%s) - t0 )) s: HTTP $code last_probe_status=$status last_seen_at=$seen probed in the last 3 min=$fresh"
echo "  problems: $(json_get "$WORK/b" 'd["problems"]')"
check "last_probe_status" "$status" ok
if [[ -n "$seen" && "$seen" != None ]]; then pass "last_seen_at set ($seen)"; else fail "last_seen_at is null"; fi
check "probe rounds keep running (last_probe_at < 3 min)" "$fresh" True
code=$(api "$JADH" "$P/api/v1/apps/$PORTAL_APP_ID/health")
check "portal's own app health" "$code" 200
check "portal host probed ok (control sample)" "$(json_get "$WORK/b" 'd["app"]["deployments"][0]["last_probe_status"]')" ok

section "18. Portal pods: stable, and logs hold no assertion or cookie values"
echo "  $($K -n portal get pods -l app.kubernetes.io/name=portal -o jsonpath='{range .items[*]}{.metadata.name}{" ready="}{.status.containerStatuses[0].ready}{" restarts="}{.status.containerStatuses[0].restartCount}{"\n"}{end}')"
check "all portal pods ready" "$($K -n portal get deploy portal -o jsonpath='{.status.readyReplicas}')" 2
restarts="$($K -n portal get pods -l app.kubernetes.io/name=portal -o jsonpath='{range .items[*]}{.status.containerStatuses[0].restartCount}{" "}{end}')"
if [[ "$restarts" =~ [1-9] ]]; then fail "restarts: $restarts"; else pass "restart counts: $restarts"; fi
check_not "no CrashLoopBackOff" "$($K -n portal get pods -o jsonpath='{.items[*].status.containerStatuses[*].state.waiting.reason}')" CrashLoop
$K -n portal logs -l app.kubernetes.io/name=portal -c portal --prefix=false --tail=-1 > "$WORK/portal.log" 2>/dev/null
echo "  portal log lines scanned: $(wc -l < "$WORK/portal.log"); WARN/ERROR lines: $(grep -cE '"level":"(WARN|ERROR)"' "$WORK/portal.log")"
check_not "no JWT-looking value (eyJ...) in the portal logs" "$(cat "$WORK/portal.log")" "eyJ"
check_not "no cookie names in the portal logs" "$(cat "$WORK/portal.log")" "__Host-Http-platform"
check_not "no Platform-Assertion in the portal logs" "$(cat "$WORK/portal.log")" "Platform-Assertion"
for jar in "$JAD" "$JP"; do
  for name in __Host-Http-platform_app __Host-Http-platform_session; do
    v="$(awk -v n="$name" '$6==n{print $7}' "$jar")"
    [[ -n "$v" ]] && { grep -qF "$v" "$WORK/portal.log" && fail "$name value found in the portal logs" || pass "$name value not in the portal logs"; }
  done
done
# The WARN of the OPTIONS preflight of section 15 (no assertion, 401) is expected; anything else (probe findings) is not.
$K -n portal logs -l app.kubernetes.io/name=portal -c portal --prefix=false --tail=-1 --since-time="$SINCE_13" > "$WORK/portal-since.log" 2>/dev/null
echo "  lines since section 13: $(wc -l < "$WORK/portal-since.log")"
check_not "no WARN since section 13 besides the rejected preflight (probe findings, inconclusive rounds)" "$(grep '"level":"WARN"' "$WORK/portal-since.log" | grep -v 'platform assertion rejected')" '"level":"WARN"'
check_not "no ERROR level (PORTAL_PROBE_INSECURE_TLS and PORTAL_ALLOW_PRIVILEGED_DB_LOGIN are off)" "$(cat "$WORK/portal.log")" '"level":"ERROR"'

section "19. Monitoring: both processes scraped, rules loaded and healthy, Grafana up with the dashboard; the metrics port is Prometheus-only"
PROM=/api/v1/namespaces/monitoring/services/monitoring-prometheus:9090/proxy # apiserver service proxy: from the node, never filtered
prom_get() { $K get --raw "$PROM$1" 2>/dev/null; }
prom_value() { # prom_value <promql>: first sample value, empty when there is none
  prom_get "/api/v1/query?query=$(python3 -c 'import sys,urllib.parse as u;print(u.quote(sys.argv[1]))' "$1")" > "$WORK/q" && json_get "$WORK/q" 'd["data"]["result"][0]["value"][1]'
}
check "Prometheus is ready" "$(prom_get /-/ready)" "Ready"
WANT_PA="$($K -n platform get deploy platform-api -o jsonpath='{.status.readyReplicas}')"
WANT_PO="$($K -n portal get deploy portal -o jsonpath='{.status.readyReplicas}')"
# Section 12 replaced a platform-api pod: wait for the target list to follow the pods (service discovery + first scrape).
end=$(( $(date +%s) + 150 )); up_pa=""; up_po=""
while (( $(date +%s) < end )); do
  prom_get "/api/v1/targets?state=active" > "$WORK/targets"
  up_pa="$(json_get "$WORK/targets" 'sum(1 for t in d["data"]["activeTargets"] if t["labels"].get("job")=="platform-api" and t["health"]=="up")')"
  up_po="$(json_get "$WORK/targets" 'sum(1 for t in d["data"]["activeTargets"] if t["labels"].get("job")=="portal" and t["health"]=="up")')"
  all_pa="$(json_get "$WORK/targets" 'sum(1 for t in d["data"]["activeTargets"] if t["labels"].get("job")=="platform-api")')"
  all_po="$(json_get "$WORK/targets" 'sum(1 for t in d["data"]["activeTargets"] if t["labels"].get("job")=="portal")')"
  [[ "$up_pa" == "$WANT_PA" && "$all_pa" == "$WANT_PA" && "$up_po" == "$WANT_PO" && "$all_po" == "$WANT_PO" ]] && break
  sleep 3
done
echo "  targets: platform-api up=$up_pa/$all_pa (ready pods $WANT_PA), portal up=$up_po/$all_po (ready pods $WANT_PO)"
check "platform-api targets: one per ready pod, all up" "$up_pa/$all_pa" "$WANT_PA/$WANT_PA"
check "portal targets: one per ready pod, all up" "$up_po/$all_po" "$WANT_PO/$WANT_PO"
check "scrape jobs: only the two processes and Prometheus itself" "$(json_get "$WORK/targets" 'sorted(set(t["labels"]["job"] for t in d["data"]["activeTargets"]))')" "['monitoring-prometheus', 'platform-api', 'portal']"
check "both processes are scraped on the metrics port" "$(json_get "$WORK/targets" 'all(t["scrapeUrl"].endswith(":9090/metrics") for t in d["data"]["activeTargets"] if t["labels"]["job"] in ("platform-api","portal"))')" True

WANT_RULES="$(grep -c '^ *- alert:' platform-gitops/monitoring/rules/portal-platform.rules.yaml)"
end=$(( $(date +%s) + 240 )); loaded=""; bad=""
while (( $(date +%s) < end )); do
  prom_get /api/v1/rules > "$WORK/rules"
  loaded="$(json_get "$WORK/rules" 'sum(len(g["rules"]) for g in d["data"]["groups"] if g["name"].startswith("portal-platform."))')"
  bad="$(json_get "$WORK/rules" 'sum(1 for g in d["data"]["groups"] for r in g["rules"] if r["health"]!="ok" or r.get("lastError"))')"
  [[ "$loaded" == "$WANT_RULES" && "$bad" == 0 ]] && break
  sleep 3
done
echo "  rules loaded: $loaded (rules file: $WANT_RULES), unhealthy: $bad"
check "alert rules loaded (as many as the plain rules file)" "$loaded" "$WANT_RULES"
check "no rule in error or never evaluated" "$bad" 0
firing="$(json_get "$WORK/rules" '[r["name"] for g in d["data"]["groups"] for r in g["rules"] if r["state"]=="firing"]')"
echo "  alerts firing now (informational): $firing"
for a in UnprotectedDeployment ForwardAuthDown ForwardAuthErrorsHigh SigningKeyNotLoaded SigningKeyKEKMismatch; do
  check_not "critical alert $a is not firing in a healthy platform" "$firing" "$a"
done
# Samples really flow: the traffic of the sections above is visible in both processes (30 s scrape interval).
for q in 'sum(platform_forwardauth_decisions_total)' 'sum(portal_http_requests_total{route="GET /api/v1/me"})'; do
  v="$(prom_value "$q")"; echo "  $q = ${v:-<none>}"
  if [[ -n "$v" ]] && python3 -c 'import sys;sys.exit(0 if float(sys.argv[1])>0 else 1)' "$v"; then pass "$q > 0"; else fail "$q is not > 0 (got '${v:-none}')"; fi
done

echo "  -- Grafana: health and the unauthenticated API through a port-forward (inside the toolbox); the dashboard and the datasource"
echo "     health as the platform admin through Traefik (basic auth is off, the assertion is the only credential)"
$K -n monitoring rollout status deploy/monitoring-grafana --timeout=120s >/dev/null
./tk sh -c 'cd /workspace/.tools/tmp/'"$(basename "$GW")"' && rm -f health.json anon.code
  kubectl -n monitoring port-forward deploy/monitoring-grafana 13000:3000 >pf.log 2>&1 &
  pf=$!
  B=http://127.0.0.1:13000
  for i in $(seq 1 60); do curl -s -o health.json --max-time 2 "$B/api/health" && break; sleep 0.5; done
  curl -s -o /dev/null -w "%{http_code}" "$B/api/dashboards/uid/portal-platform" > anon.code
  kill $pf' >/dev/null 2>&1
G_HOST=grafana.apps.wysted.dev
G=https://$G_HOST
GRAFANA_APP_ID=app_01J9Z3K7WQ4M8R2T5XV6N0B1HD # seed.sql and expect_claims in monitoring/values.yaml
JGA="$WORK/grafana-admin"; login "$JGA" admin@example.com "$G" >/dev/null
api "$JGA" "$G/api/dashboards/uid/portal-platform" >/dev/null; cp "$WORK/b" "$GW/dash.json"
api "$JGA" "$G/api/datasources/uid/prometheus/health" >/dev/null; cp "$WORK/b" "$GW/ds.json"
check "Grafana health: database ok" "$(json_get "$GW/health.json" 'd["database"]')" ok
check "Grafana API without credentials (port-forward, no assertion)" "$(<"$GW/anon.code")" 401
check "dashboard 'Portal platform' is provisioned" "$(json_get "$GW/dash.json" 'd["dashboard"]["title"] + " provisioned=" + str(d["meta"]["provisioned"])')" "Portal platform provisioned=True"
check "dashboard has every panel of the versioned JSON" "$(json_get "$GW/dash.json" 'len(d["dashboard"]["panels"])')" "$(json_get platform-gitops/monitoring/dashboards/portal-platform.json 'len(d["panels"])')"
check "Grafana reaches Prometheus (datasource health)" "$(json_get "$GW/ds.json" 'd["status"]')" OK

echo "  -- NetworkPolicy of the monitoring namespace. probe-other has an allow-all EGRESS policy of its own, so what it cannot reach"
echo "     is blocked by the INGRESS policy of the destination (positive controls first); probe-prom / probe-grafana wear the labels of"
echo "     the real pods and exercise their EGRESS policies"
PROM_IP="$(pod_ip monitoring prometheus-monitoring-prometheus-0)"
API_IP="$($K get endpoints kubernetes -o jsonpath='{.subsets[0].addresses[0].ip}' 2>/dev/null)"
TRAEFIK_IP="$($K -n traefik get pod -l app.kubernetes.io/name=traefik,app.kubernetes.io/instance=traefik-traefik -o jsonpath='{.items[0].status.podIP}')"
echo "  prometheus=$PROM_IP kube-apiserver=$API_IP traefik pod=$TRAEFIK_IP"
# The source of every destination-ingress negative must be able to send: probe-other reaches ports that are open and
# unfiltered by any ingress policy (the API server, a Traefik pod).
check "probe-other (allow-all egress) -> kube-apiserver $API_IP:6443 (positive: it can send)" "$(probe_tcp monitoring probe-other "$API_IP" 6443)" connected
check "probe-other (allow-all egress) -> Traefik pod :8443 (positive: it can send)" "$(probe_tcp monitoring probe-other "$TRAEFIK_IP" 8443)" connected
for pod in $PA_PODS; do
  ip="$(pod_ip platform "$pod")"
  check "monitoring Prometheus-labelled pod -> platform-api $pod:9090/metrics (positive)" "$(wait_probe monitoring probe-prom http://$ip:9090/metrics 200)" 200
  check "[ingress] probe-other -> platform-api $pod:9090 (not a Prometheus pod)" "$(probe monitoring probe-other http://$ip:9090/metrics)" 000
  check "[ingress] probe-other -> platform-api $pod:8083 (JWKS listener: app namespaces only)" "$(probe_tlsk monitoring probe-other https://$ip:8083/.well-known/jwks.json)" 000
  check "[ingress] probe-other -> platform-api $pod:8081 (internal listener: Traefik only)" "$(probe monitoring probe-other http://$ip:8081/healthz)" 000
  check "[ingress] probe-other -> platform-api $pod:8080 (public listener: Traefik only)" "$(probe monitoring probe-other http://$ip:8080/login)" 000
  check "[egress] Prometheus-labelled pod -> platform-api $pod:8083 (JWKS listener)" "$(probe_tlsk monitoring probe-prom https://$ip:8083/.well-known/jwks.json)" 000
  check "[egress] Prometheus-labelled pod -> platform-api $pod:8081 (internal listener)" "$(probe monitoring probe-prom http://$ip:8081/healthz)" 000
  check "[egress] Prometheus-labelled pod -> platform-api $pod:8080 (public listener)" "$(probe monitoring probe-prom http://$ip:8080/login)" 000
  check "[egress] Grafana-labelled pod -> platform-api $pod:9090" "$(probe monitoring probe-grafana http://$ip:9090/metrics)" 000
done
for pod in $PO_PODS; do
  ip="$(pod_ip portal "$pod")"
  check "monitoring Prometheus-labelled pod -> portal $pod:9090/metrics (positive)" "$(wait_probe monitoring probe-prom http://$ip:9090/metrics 200)" 200
  check "[ingress] probe-other -> portal $pod:9090 (not a Prometheus pod)" "$(probe monitoring probe-other http://$ip:9090/metrics)" 000
  check "[ingress] probe-other -> portal $pod:8080 (public listener: Traefik only)" "$(probe monitoring probe-other http://$ip:8080/api/v1/me)" 000
  check "[ingress] probe-other -> portal $pod:8081 (internal listener: kubelet only)" "$(probe monitoring probe-other http://$ip:8081/healthz)" 000
  check "[egress] Prometheus-labelled pod -> portal $pod:8080 (public listener)" "$(probe monitoring probe-prom http://$ip:8080/api/v1/me)" 000
  check "[egress] Grafana-labelled pod -> portal $pod:9090" "$(probe monitoring probe-grafana http://$ip:9090/metrics)" 000
done
check "Grafana-labelled pod -> Prometheus $PROM_IP:9090 (positive)" "$(wait_probe monitoring probe-grafana http://$PROM_IP:9090/-/ready 200)" 200
check "[ingress] non-Grafana pod (probe-other) -> Prometheus:9090" "$(probe monitoring probe-other http://$PROM_IP:9090/-/ready)" 000
# Egress, each with a positive from an unrestricted pod (smoke-app has no policies): Traefik pod port 8443 and the API.
check "smoke-app (no policies) -> Traefik pod :8443 (positive)" "$(probe_tcp smoke-app probe-open "$TRAEFIK_IP" 8443)" connected
check "[egress] Prometheus-labelled pod -> Traefik pod :8443 (egress is minimal)" "$(probe_tcp monitoring probe-prom "$TRAEFIK_IP" 8443)" blocked
check "[egress] Grafana-labelled pod -> Traefik pod :8443 (egress is minimal)" "$(probe_tcp monitoring probe-grafana "$TRAEFIK_IP" 8443)" blocked
check "smoke-app (no policies) -> kube-apiserver $API_IP:6443 (positive)" "$(probe_tcp smoke-app probe-open "$API_IP" 6443)" connected
check "Prometheus-labelled pod -> kube-apiserver :6443 (discovery)" "$(probe_tcp monitoring probe-prom "$API_IP" 6443)" connected
check "[egress] Grafana-labelled pod -> kube-apiserver :6443" "$(probe_tcp monitoring probe-grafana "$API_IP" 6443)" blocked
echo "  -- Hardening of the stack"
check_not "Prometheus runs without --web.enable-lifecycle" "$($K -n monitoring get sts prometheus-monitoring-prometheus -o jsonpath='{.spec.template.spec.containers[*].args}')" "enable-lifecycle"
check "POST /-/reload is refused (lifecycle API off; a Grafana-labelled pod asks)" "$($K -n monitoring exec probe-grafana -- curl -s -o /dev/null --max-time 4 -w '%{http_code}' -X POST "http://$PROM_IP:9090/-/reload" 2>/dev/null || true)" 403
check "Grafana pod mounts no ServiceAccount token" "$($K -n monitoring get deploy monitoring-grafana -o jsonpath='{.spec.template.spec.automountServiceAccountToken}')" false
check "API egress policy lists /32 endpoints only" "$($K -n monitoring get networkpolicy allow-egress-apiserver -o jsonpath='{.spec.egress[0].to[*].ipBlock.cidr}' | tr ' ' '\n' | grep -vc '/32$' || true)" 0

section "19b. Grafana behind ForwardAuth: JWT auth from Platform-Assertion, roles from the grant, no other way in"
# assertion_for <jar> <host> <outfile>: asks one platform-api replica (from the Traefik-labelled probe pod, like section 5)
# for the ForwardAuth decision with the app cookie of <jar> for <host>. Prints the status; the Platform-Assertion it returns
# (a real one, minted for that host's app, valid 90 s) goes to <outfile> and is never printed.
assertion_for() {
  local jar="$1" host="$2" out="$3" ip cookie
  ip="$($K -n platform get pods -l app.kubernetes.io/name=platform-api -o jsonpath='{.items[0].status.podIP}')"
  cookie="$(awk -v h="$host" '$1 ~ h && $6=="__Host-Http-platform_app"{print $7}' "$jar")"
  rm -f "$out"
  $K -n traefik exec probe-traefik -- curl -s -o /dev/null --max-time 4 -D - -H "X-Forwarded-Host: $host" -H "X-Forwarded-Method: GET" \
    -H "X-Forwarded-Uri: /api/user" -H "Accept: application/json" -H "Sec-Fetch-Mode: cors" -H "Cookie: __Host-Http-platform_app=$cookie" \
    "http://$ip:8081/forwardauth" 2>/dev/null | tr -d '\r' \
    | awk -v out="$out" 'NR==1{st=$2} tolower($1)=="platform-assertion:"{print $2 > out} END{print st}'
}
# jwt_claim <file> <claim>: claim of the (unverified) assertion in <file>.
jwt_claim() { python3 -I -c 'import base64,json,sys;t=open(sys.argv[1]).read().split()[0].split(".")[1];print(json.loads(base64.urlsafe_b64decode(t+"="*(-len(t)%4))).get(sys.argv[2],""))' "$1" "$2"; }
UUID_RE='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
is_uuid() { [[ "$1" =~ $UUID_RE ]] && echo uuid || echo "not-a-uuid:$1"; }

GU="$WORK/grafana-user"
echo "  -- admin (observability/admin) -> Grafana Admin; identity = sub (UUID), the email is never the identity"
code=$(api "$JGA" "$G/api/user"); check "admin: GET /api/user" "$code" 200
cp "$WORK/b" "$GW/admin-user.json"
check "admin: login is the UUID" "$(is_uuid "$(json_get "$GW/admin-user.json" 'd["login"]')")" uuid
check "admin: assertion is for the grafana app" "$(assertion_for "$JGA" "$G_HOST" "$GW/admin.jwt")" 200
check "admin: login == sub of the assertion" "$([[ "$(json_get "$GW/admin-user.json" 'd["login"]')" == "$(jwt_claim "$GW/admin.jwt" sub)" ]] && echo equal || echo different)" equal
check_not "admin: the assertion's email is not stored in Grafana" "$(json_get "$GW/admin-user.json" 'd["email"]')" example.com
code=$(api "$JGA" "$G/api/org"); check "admin: GET /api/org" "$code" 200
api "$JGA" "$G/api/user/orgs" >/dev/null; check "admin: org role" "$(json_get "$WORK/b" '",".join(o["role"] for o in d)')" Admin
check "admin: not a Grafana server admin (allow_assign_grafana_admin off)" "$(json_get "$GW/admin-user.json" 'd["isGrafanaAdmin"]')" False
echo "  -- alice (observability/viewer) -> Viewer"
login "$GU" alice@example.com "$G" >/dev/null
code=$(api "$GU" "$G/api/user"); check "alice: GET /api/user" "$code" 200
check "alice: login is the UUID" "$(is_uuid "$(json_get "$WORK/b" 'd["login"]')")" uuid
check_not "alice: the assertion's email is not stored in Grafana" "$(json_get "$WORK/b" 'd["email"]')" example.com
api "$GU" "$G/api/user/orgs" >/dev/null; check "alice: org role" "$(json_get "$WORK/b" '",".join(o["role"] for o in d)')" Viewer
code=$(mutate "$GU" POST "$G/api/dashboards/db" same-origin application/json '{"dashboard":{"title":"x"}}'); check "alice: cannot create a dashboard" "$code" 403
echo "  -- carol (no grant on observability) -> 403 from ForwardAuth"
JGC="$WORK/grafana-carol"; code=$(login "$JGC" carol@example.com "$G"); check "carol: opening Grafana after the login" "$code" 403
code=$(api "$JGC" "$G/api/user"); check "carol: GET /api/user" "$code" 403
echo "  -- bob (only role on observability: auditor, unmapped) -> ForwardAuth allows, Grafana refuses (role_attribute_strict)"
JGB="$WORK/grafana-bob"; login "$JGB" bob@example.com "$G" >/dev/null
check "bob: ForwardAuth decision for grafana" "$(assertion_for "$JGB" "$G_HOST" "$GW/bob.jwt")" 200
check "bob: the assertion carries role auditor only" "$(jwt_claim "$GW/bob.jwt" roles)" "['auditor']"
code=$(api "$JGB" "$G/api/user"); check "bob: GET /api/user through Traefik (refused by Grafana, not by ForwardAuth)" "$code" 403
check "bob: Grafana's own refusal (jwt.invalid_role)" "$(json_get "$WORK/b" 'd["messageId"]')" jwt.invalid_role
code=$(api "$JGB" "$G/api/org"); check "bob: GET /api/org" "$code" 403
code=$(nav "$JGB" "$G/"); echo "  GET / -> $code Location: $(header location)"
check "bob: opening Grafana never yields a page (redirected to the refusal, no session)" "$code" 302
echo "  -- anonymous / client-sent assertion"
code=$(api "$WORK/grafana-anon" "$G/api/user"); check "anonymous API request" "$code" 401
FORGED="$(python3 -c 'import base64,json;b=lambda o:base64.urlsafe_b64encode(json.dumps(o).encode()).rstrip(b"=").decode();print(b({"alg":"none","typ":"JWT"})+"."+b({"iss":"https://auth.apps.wysted.dev","aud":"'$GRAFANA_APP_ID'","sub":"forged","email":"forged@example.com","roles":["admin"]})+".")')"
code=$(api "$WORK/grafana-anon" -H "Platform-Assertion: $FORGED" "$G/api/user"); check "unauthenticated request carrying an assertion is rejected at the door (no session)" "$code" 401
code=$(api "$GU" -H "Platform-Assertion: $FORGED" "$G/api/user"); check "alice's request carrying an assertion: served with the platform-issued one" "$code" 200
check "still alice's UUID, not the client's sub" "$(is_uuid "$(json_get "$WORK/b" 'd["login"]')")" uuid
echo "     (that the client's header is stripped before the app is proven at the auth host and on hello-a: section 7)"
echo "  -- direct to Grafana (port-forward, bypassing Traefik): audience binding, no local login, basic auth off"
# Fresh assertions (90 s): one minted for hello-a (another app's aud), one for Grafana (positive control).
JHA="$WORK/ja19"; login "$JHA" alice@example.com "$A" >/dev/null
check "alice: ForwardAuth decision for hello-a" "$(assertion_for "$JHA" "$A_HOST" "$GW/hello.jwt")" 200
check "hello-a assertion has aud = hello-a" "$(jwt_claim "$GW/hello.jwt" aud)" "$HELLO_A_ID"
check "alice: ForwardAuth decision for grafana" "$(assertion_for "$GU" "$G_HOST" "$GW/graf.jwt")" 200
check "grafana assertion has aud = grafana" "$(jwt_claim "$GW/graf.jwt" aud)" "$GRAFANA_APP_ID"
printf 'header = "Platform-Assertion: %s"\n' "$(<"$GW/hello.jwt")" > "$GW/curlrc-hello"
printf 'header = "Platform-Assertion: %s"\n' "$(<"$GW/graf.jwt")" > "$GW/curlrc-graf"
chmod 600 "$GW"/curlrc-*
# The password file must be the Secret's content: otherwise a 401 would only say "wrong password", not "basic auth disabled".
PW_FILE_SHA="$(sha256sum < .tools/secrets/k3d-grafana-admin-password | cut -d' ' -f1)"
PW_SECRET_SHA="$($K -n monitoring get secret grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d | sha256sum | cut -d' ' -f1)"
check "positive control: the admin password file matches Secret grafana-admin (hashes compared)" "$([[ -n $PW_FILE_SHA && $PW_FILE_SHA == "$PW_SECRET_SHA" ]] && echo match || echo mismatch)" match
./tk sh -c 'cd /workspace/.tools/tmp/'"$(basename "$GW")"' && printf "user = \"admin:%s\"\n" "$(cat /workspace/.tools/secrets/k3d-grafana-admin-password)" > curlrc2 && chmod 600 curlrc2
  rm -f aud.code ctl.code ctl.json basic.code form.code form.json login.code root.loc
  kubectl -n monitoring port-forward deploy/monitoring-grafana 13001:3000 >pf2.log 2>&1 &
  pf=$!
  U=http://127.0.0.1:13001
  for i in $(seq 1 60); do curl -s -o /dev/null --max-time 2 $U/api/health && break; sleep 0.5; done
  curl -s -K curlrc-hello -o /dev/null -w "%{http_code}" $U/api/user > aud.code
  curl -s -K curlrc-graf -o ctl.json -w "%{http_code}" $U/api/user > ctl.code
  curl -s -K curlrc2 -o /dev/null -w "%{http_code}" $U/api/user > basic.code
  curl -s -o form.json -w "%{http_code}" -X POST -H "Content-Type: application/json" -d "{\"user\":\"admin\",\"password\":\"x\"}" $U/login > form.code
  curl -s -o /dev/null -w "%{redirect_url}" $U/ > root.loc
  kill $pf; rm -f curlrc2' >/dev/null 2>&1
check "assertion minted for hello-a (valid, wrong aud) does not authenticate" "$(<"$GW/aud.code")" 401
check "positive control: the Grafana-aud assertion authenticates through the same path" "$(<"$GW/ctl.code")" 200
check "positive control: it is alice (login == sub)" "$([[ "$(json_get "$GW/ctl.json" 'd["login"]')" == "$(jwt_claim "$GW/graf.jwt" sub)" ]] && echo equal || echo different)" equal
check "basic auth with the admin credentials is refused (password verified above; port-forward, no assertion)" "$(<"$GW/basic.code")" 401
check "POST /login with the form is refused (auth.client.notConfigured)" "$(<"$GW/form.code")" 400
check "POST /login: no session cookie or user in the answer" "$(json_get "$GW/form.json" 'd.get("message")')" BadRequest
api "$JGA" "$G/api/frontend/settings" >/dev/null; check "frontend settings: disableLoginForm is true" "$(json_get "$WORK/b" 'd["disableLoginForm"]')" True
check "unauthenticated / redirects to /login (which renders no form)" "$(<"$GW/root.loc")" "/login"
rm -f "$GW"/curlrc-* "$GW"/*.jwt
echo "  -- NetworkPolicy: Grafana :3000 is reachable from Traefik only"
GRAFANA_IP="$(pod_ip monitoring "$($K -n monitoring get pod -l app.kubernetes.io/name=grafana -o jsonpath='{.items[0].metadata.name}')")"
check "Traefik-labelled pod -> Grafana $GRAFANA_IP:3000 (positive)" "$(wait_probe traefik probe-traefik http://$GRAFANA_IP:3000/api/health 200)" 200
check "[ingress] other namespace (default) -> Grafana :3000" "$(probe default probe-default http://$GRAFANA_IP:3000/api/health)" 000
check "[ingress] app-namespace pod (smoke-app) -> Grafana :3000" "$(probe smoke-app probe-open http://$GRAFANA_IP:3000/api/health)" 000
check "[ingress] same namespace (probe-other) -> Grafana :3000" "$(probe monitoring probe-other http://$GRAFANA_IP:3000/api/health)" 000
check "[service] default -> monitoring-grafana.monitoring:80" "$(probe default probe-default http://monitoring-grafana.monitoring.svc.cluster.local/api/health)" 000
echo "  -- NetworkPolicy: Grafana -> JWKS only through its own rule (platform side)"
check "probe-other (monitoring, not Grafana) -> platform-api JWKS :8083" "$($K -n monitoring exec probe-other -- curl -s -o /dev/null --max-time 4 -w '%{http_code}' -k https://platform-api-jwks.platform.svc.cluster.local:8083/.well-known/jwks.json 2>/dev/null || true)" 000
check "probe-grafana (Grafana labels) -> platform-api JWKS :8083" "$($K -n monitoring exec probe-grafana -- curl -s -o /dev/null --max-time 4 -w '%{http_code}' -k https://platform-api-jwks.platform.svc.cluster.local:8083/.well-known/jwks.json 2>/dev/null || true)" 200

section "20. Web app through Traefik: HTML with the startup-computed CSP, immutable assets, JSON 404s, ForwardAuth, method guards"
# inline_hash <html file>: sha256 (base64) of every EXECUTABLE inline <script> body, one per line (what a browser compares
# to the CSP). Data blocks (type="application/json", e.g. __NUXT_DATA__) never execute and are not hashed.
inline_hash() { python3 -c '
import base64, hashlib, re, sys
html = open(sys.argv[1], encoding="utf-8").read()
for m in re.finditer(r"<script(?![^>]*\bsrc=)([^>]*)>(.*?)</script>", html, re.S):
    t = re.search(r"\btype=[\"\x27]?([^\"\x27\s>]*)", m.group(1))
    if t and t.group(1) not in ("module", "text/javascript", "application/javascript"):
        continue
    print(base64.b64encode(hashlib.sha256(m.group(2).encode()).digest()).decode())' "$1"; }
JW="$WORK/portal-web"
login "$JW" alice@example.com "$P" >/dev/null
code=$(nav "$JW" "$P/"); check "GET / (navigation, session)" "$code" 200
cp "$WORK/b" "$WORK/web-home.html"; hdrs="$(tr -d '\r' < "$WORK/h" | tr A-Z a-z)"
check "content type" "$(header content-type)" "text/html"
check "Cache-Control no-cache" "$(header cache-control)" "no-cache"
csp="$(header content-security-policy)"; echo "  csp: $csp"
for d in "default-src 'none'" "script-src 'self' 'sha256-" "style-src 'self';" "img-src 'self';" "font-src 'self';" "connect-src 'self';" \
         "form-action 'self' $AUTH;" "frame-ancestors 'none'" "base-uri 'none'"; do check "CSP directive" "$csp" "$d"; done
check_not "CSP has no unsafe-inline/unsafe-eval" "$csp" "unsafe-"
hashes="$(inline_hash "$WORK/web-home.html")"
check "exactly one inline script" "$(echo "$hashes" | grep -c .)" 1
check "CSP sha256 matches the inline script of the served HTML" "$csp" "'sha256-$hashes'"
for h in "strict-transport-security: max-age=63072000; includesubdomains" "x-frame-options: deny" "x-content-type-options: nosniff" \
         "cross-origin-opener-policy: same-origin" "cross-origin-resource-policy: same-origin" "referrer-policy: no-referrer"; do check "header" "$hdrs" "$h"; done
check_not "no Access-Control-* on the HTML" "$hdrs" "access-control-"
home_etag="$(header etag)"
code=$(nav "$JW" "$P/admin/groups/some-id"); check "deep client route /admin/groups/some-id" "$code" 200
check "deep route serves the same body as /" "$(cmp -s "$WORK/b" "$WORK/web-home.html" && echo same)" same
check "deep route has the same CSP" "$(header content-security-policy)" "$csp"
check "deep route Cache-Control no-cache" "$(header cache-control)" "no-cache"
code=$(nav "$JW" -H "If-None-Match: $home_etag" "$P/"); check "conditional GET / with the ETag" "$code" 304
asset="$(grep -o '/_nuxt/[^"]*\.js' "$WORK/web-home.html" | head -1)"; echo "  asset: $asset"
[[ -n "$asset" ]] && pass "an /_nuxt/*.js asset is referenced by the HTML" || fail "no /_nuxt/*.js asset in the served HTML"
code=$(api "$JW" "$P$asset"); check "GET $asset" "$code" 200
check "asset content type is JavaScript" "$(header content-type)" "javascript"
check "asset Cache-Control immutable" "$(header cache-control)" "public, max-age=31536000, immutable"
check "asset keeps the API CSP" "$(header content-security-policy)" "default-src 'none'; frame-ancestors 'none'"
check "asset has nosniff" "$(header x-content-type-options)" nosniff
css="$(grep -o '/_nuxt/[^"]*\.css' "$WORK/web-home.html" | head -1)"
code=$(api "$JW" "$P$css"); check "GET $css" "$code" 200
check "stylesheet content type" "$(header content-type)" "text/css"
code=$(api "$JW" "$P/_nuxt/nope.js"); check "missing /_nuxt/nope.js" "$code" 404
check "404 is problem+json" "$(header content-type)" "application/problem+json"
check "404 body" "$(cat "$WORK/b")" '"status":404'
check_not "a missing asset is never cached as immutable" "$(header cache-control)" immutable
code=$(api "$JW" "$P/api/unknown"); check "GET /api/unknown" "$code" 404
check "unknown API route is problem+json" "$(header content-type)" "application/problem+json"
check "unknown API route keeps the API CSP" "$(header content-security-policy)" "default-src 'none'; frame-ancestors 'none'"
code=$(api "$JW" "$P/missing.json"); check "GET a path that looks like a file is a JSON 404, not the SPA" "$code" 404
echo "  -- Method guards (JSON/CrossOrigin guards run before routing)"
code=$(mutate "$JW" POST "$P/" same-origin application/json '{}'); check "POST / same-origin + JSON" "$code" 405
check "405 lists the allowed methods" "$(header allow)" "GET, HEAD"
code=$(mutate "$JW" POST "$P/" same-origin -); check "POST / without Content-Type (JSON guard first)" "$code" 415
code=$(mutate "$JW" POST "$P/" cross-site application/json '{}'); check "POST / cross-site (CrossOriginProtection first)" "$code" 403
echo "  -- ForwardAuth applies to the web app like to the API"
code=$(nav "$WORK/web-anon" "$P/"); check "navigation to / without session" "$code" 302
check "redirects to the auth host login" "$(header location)" "$AUTH/login?"
code=$(nav "$WORK/web-anon" "$P/admin/groups/some-id"); check "navigation to a deep route without session" "$code" 302
check "deep route carries rd with the path" "$(header location)" "rd=https%3A%2F%2F$P_HOST%2Fadmin%2Fgroups%2Fsome-id"
code=$(api "$WORK/web-anon" "$P$asset"); check "asset fetch without session (non-navigation)" "$code" 401
code=$(api "$WORK/web-anon" "$P/api/unknown"); check "unknown API route without session" "$code" 401

section "21. Real browser (Chromium/Playwright in Docker): fake login, / renders, error page for an unknown route, zero CSP violations"
# Last section and part of the smoke (not a separate command): it shares the smoke's contract (PASS/FAIL lines, one exit
# code, cluster as found) and the browser is the only check of what the CSP does to the SPA (inline styles/scripts). Cost:
# needs the host docker and the pinned Playwright image (pulled once). SKIP_BROWSER_CHECK=1 skips it explicitly.
if [[ "${SKIP_BROWSER_CHECK:-}" == 1 ]]; then
  echo "  SKIP: SKIP_BROWSER_CHECK=1"; BROWSER_SKIPPED=1
else
  "$REL/browser-check/browser-check.sh" > "$WORK/browser.log" 2>&1; rc=$?
  cat "$WORK/browser.log"
  bf="$(grep -c '^  FAIL:' "$WORK/browser.log")"
  if (( bf > 0 )); then
    while IFS= read -r line; do fail "browser: ${line#  FAIL: }"; done < <(grep '^  FAIL:' "$WORK/browser.log")
  elif (( rc != 0 )); then
    fail "browser check exited $rc without a verdict (see the output above)"
  else
    pass "browser check finished with $(grep -c '^  PASS:' "$WORK/browser.log") passing checks"
  fi
fi

section "22. API tokens end to end (contract block 6, part 3): Authorization reaches platform-api on the portal host only and never the portal"
SINCE_22="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
# Bearer values never reach stdout, argv or $WORK/b-derived output: they live in header files (curl -H @file) and are
# compared by grep -F -f; a failing check never prints a body that carries one (see body_clean). The whole section follows the contract's checks (a)-(g) of "Implementation parts", item 3.
tokfile() { echo "$WORK/auth.$1"; }
# bcall <token name> <curl args...>: portal-style API call carrying that token and NO cookie.
bcall() { local n="$1"; shift; req "$WORK/tok-none" -H 'Accept: application/json' -H 'Sec-Fetch-Mode: cors' -H @"$(tokfile "$n")" "$@"; }
# mk_token <jar> <url> <token name> <scope>: creates a token through the real portal API with a browser-like session.
mk_token() {
  local code; code=$(mutate "$1" POST "$2" same-origin application/json "{\"name\":\"smoke-$3\",\"scope\":\"$4\",\"expires_in\":3600}")
  if [[ "$code" != 201 ]]; then fail "create token $3 ($4): status $code"; return 1; fi
  printf 'Authorization: Bearer %s\n' "$(json_get "$WORK/b" 'd["secret"]')" > "$(tokfile "$3")"
  cut -d' ' -f3 "$(tokfile "$3")" >> "$WORK/secrets.txt"
  printf '%s' "$(json_get "$WORK/b" 'd["id"]')" > "$WORK/tid.$3"
  pass "token $3 ($4) created"
}
tid() { cat "$WORK/tid.$1"; }
# body_clean: the last body without Authorization lines (a whoami echo holds the Bearer that was sent), for check/check_not.
body_clean() { grep -vi '^authorization:' "$WORK/b"; }
# leaks <file>: true if the file holds any Bearer value of this section (secrets.txt).
leaks() { grep -qF -f "$WORK/secrets.txt" "$1"; }
: > "$WORK/secrets.txt"

JTA="$WORK/tok-alice"; JTM="$WORK/tok-admin"; JTH="$WORK/tok-hello" # no earlier hello-a jar of alice is still valid here (section 11's session is gone by now)
echo "  -- setup: sessions, fixtures, tokens"
$K apply -f "$REL/smoke-whoami.yaml" -f "$REL/smoke-whoami-portal.yaml" >/dev/null
login "$JTA" alice@example.com "$P" >/dev/null; login "$JTM" admin@example.com "$P" >/dev/null; login "$JTH" alice@example.com "$A" >/dev/null
# Re-runnable: revoke leftovers of an interrupted run (the robot's live tokens too) and re-enable the robot.
drop_smoke_tokens
api "$JTA" "$P/api/v1/me/tokens" >/dev/null
for old in $(json_get "$WORK/b" '" ".join(t["id"] for t in d["tokens"] if t["name"].startswith("smoke-") and not t["revoked_at"])'); do
  mutate "$JTA" DELETE "$P/api/v1/me/tokens/$old" same-origin application/json >/dev/null
done
code=$(mutate "$JTM" POST "$P/api/v1/robots" same-origin application/json '{"name":"smoke-robot"}')
if [[ "$code" == 409 ]]; then
  api "$JTM" "$P/api/v1/robots" >/dev/null; RID="$(json_get "$WORK/b" '[r["id"] for r in d["robots"] if r["name"] == "smoke-robot"][0]')"
  code=$(mutate "$JTM" PATCH "$P/api/v1/robots/$RID" same-origin application/json '{"status":"active"}'); check "existing smoke-robot re-enabled" "$code" 200
else
  check "robot created by a platform admin" "$code" 201; RID="$(json_get "$WORK/b" 'd["id"]')"
fi
mk_token "$JTA" "$P/api/v1/me/tokens" alice-read read
mk_token "$JTA" "$P/api/v1/me/tokens" alice-del read
mk_token "$JTA" "$P/api/v1/me/tokens" alice-sec read
mk_token "$JTM" "$P/api/v1/robots/$RID/tokens" robot read
code=$(mutate "$JTM" POST "$P/api/v1/robots/$RID/tokens" same-origin application/json '{"name":"smoke-robot-admin","scope":"admin"}')
check "robot admin token without a manages_access grant is refused" "$code" 422
code=$(bcall alice-read -X POST -H 'Content-Type: application/json' -d '{"name":"smoke-minted","scope":"read"}' "$P/api/v1/me/tokens")
check "a token cannot mint tokens (403)" "$code" 403
$K -n portal rollout status deploy/smoke-whoami-portal --timeout=120s >/dev/null
$K -n hello-a rollout status deploy/smoke-whoami --timeout=120s >/dev/null
sleep 15 # kube-router needs a moment before a fresh pod is covered consistently

echo "  -- middleware wiring"
check "forwardauth allowlist carries Authorization" "$($K -n platform get middleware.traefik.io forwardauth -o jsonpath='{.spec.forwardAuth.authRequestHeaders}')" Authorization
# Fail closed: a listing that fails or a script that cannot parse it is a failure, never an empty (passing) invariant.
if $K get ingressroute.traefik.io -A -o json > "$WORK/routes.json" && $K get ingress.networking.k8s.io -A -o json > "$WORK/ingresses.json"; then
  inv="$(python3 - "$WORK/routes.json" "$WORK/ingresses.json" "$P_HOST" <<'PY'
import json, sys
routes = json.load(open(sys.argv[1]))["items"]
ingresses = json.load(open(sys.argv[2]))["items"]
host = sys.argv[3].lower()
STRIP = "strip-authorization"
# Deterministic here: every route of this cluster is `Host(...)` on websecure. So a route reachable from a public entrypoint
# (websecure, web or none listed = all) that has no Host( or uses HostRegexp could match the portal host without being
# recognizable: it is flagged SUSPICIOUS and fails the check. Standard Ingress objects reach the portal host only with the
# router.middlewares annotation naming portal-strip-authorization@kubernetescrd; one without Host rules is SUSPICIOUS too.
def public(eps): return not eps or any(e in ("web", "websecure") for e in eps)
chains = {}
for ir in routes:
    ns, name, spec = ir["metadata"]["namespace"], ir["metadata"]["name"], ir["spec"]
    for i, r in enumerate(spec.get("routes", [])):
        m = r["match"].lower()
        chain = [(x["name"], x.get("namespace", ns)) for x in r.get("middlewares", [])]
        chains[(ns, name, i)] = chain
        if public(spec.get("entryPoints", [])) and ("hostregexp(" in m or "host(" not in m):
            print(f"{ns}/{name} SUSPICIOUS (no Host() or HostRegexp: {r['match']})")
        elif host in m:
            print(f"{ns}/{name} {'ok' if (STRIP, ns) in chain else 'MISSING'}")
for ing in ingresses:
    ns, name = ing["metadata"]["namespace"], ing["metadata"]["name"]
    rules = ing["spec"].get("rules", [])
    ann = ing["metadata"].get("annotations", {}).get("traefik.ingress.kubernetes.io/router.middlewares", "")
    if not rules or any(not r.get("host") for r in rules):
        print(f"{ns}/{name} SUSPICIOUS (Ingress without a host rule)")
    elif any(r["host"].lower() == host for r in rules):
        print(f"{ns}/{name} {'ok' if 'portal-strip-authorization@kubernetescrd' in ann else 'MISSING'} (Ingress)")
# Same chain (order, names, namespaces) on the real route and on the smoke fixture, or (g) proves nothing about the portal.
real, fixture = chains.get(("portal", "portal", 0)), chains.get(("portal", "smoke-whoami-portal", 0))
print("CHAIN", "equal" if real is not None and real == fixture else f"DIFFERENT real={real} fixture={fixture}")
PY
)" || inv="ERROR"
else
  inv="ERROR listing"
fi
echo "  portal-host routes and flags: $(echo "$inv" | tr '\n' ';')"
check_not "the listings and the invariant script ran" "$inv" ERROR
check "the portal route is among them" "$inv" "portal/portal ok"
check_not "every route matching the portal host references strip-authorization (same namespace)" "$inv" MISSING
check_not "no route that could match the portal host unnoticed (HostRegexp, no Host(), host-less Ingress)" "$inv" SUSPICIOUS
check "the fixture runs the same middleware chain as the real portal route (order, names, namespaces)" "$inv" "CHAIN equal"

echo "  -- (a) token accepted on the portal host"
code=$(api "$JTA" "$P/api/v1/me"); ALICE_ID="$(json_get "$WORK/b" 'd["user"]["id"]')"
code=$(bcall alice-read "$P/api/v1/me"); check "human token: GET /api/v1/me" "$code" 200
check "identity is alice (email)" "$(json_get "$WORK/b" 'd["user"]["email"]')" alice@example.com
check "identity is alice (same id as the session)" "$(json_get "$WORK/b" 'd["user"]["id"]')" "$ALICE_ID"
check_not "no Set-Cookie on a token response" "$(header set-cookie)" "="
code=$(bcall robot "$P/api/v1/me"); check "robot token: GET /api/v1/me" "$code" 200
check "identity is the robot (id)" "$(json_get "$WORK/b" 'd["user"]["id"]')" "$RID"
check "identity is the robot (name)" "$(json_get "$WORK/b" 'd["user"]["name"]')" smoke-robot
check "robot has no email" "[$(json_get "$WORK/b" 'd["user"]["email"]')]" "[]"
sed 's/^Authorization: Bearer /Authorization: bearer /' "$(tokfile alice-read)" > "$(tokfile lower)"
code=$(bcall lower "$P/api/v1/me"); check "scheme is case-insensitive" "$code" 200
code=$(bcall alice-read "$P/api/v1/me/apps"); check "token reads another endpoint (apps)" "$code" 200

echo "  -- scope"
code=$(bcall alice-read -X POST -H 'Content-Type: application/json' -d '{"key":"smoke-group-rejected","name":"Smoke"}' "$P/api/v1/groups")
check "read token: POST -> 403" "$code" 403
check "403 challenge (e)" "$(header www-authenticate)" 'Bearer error="insufficient_scope"'
check_not "scope 403 is not a redirect" "$(header location)" "http"

echo "  -- (c, e) invalid, unknown and wrong-scheme credentials on the portal host"
UNK_BODY="ptk_$(python3 -c 'import base64,os;print(base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("="))')"
UNK="$UNK_BODY$(python3 -c 'import sys,zlib;print("%08x" % zlib.crc32(sys.argv[1].encode()))' "$UNK_BODY")"
printf 'Authorization: Bearer %s\n' "$UNK" > "$(tokfile unknown)"; printf '%s\n' "$UNK" >> "$WORK/secrets.txt"
printf 'Authorization: Bearer ptk_not-a-token\n' > "$(tokfile malformed)"
printf 'Authorization: Basic dXNlcjpwYXNz\n' > "$(tokfile basic)"
printf 'Authorization: Bearer\n' > "$(tokfile empty)"
for n in malformed unknown; do
  code=$(bcall $n "$P/api/v1/me"); check "$n Bearer -> 401" "$code" 401
  check "$n challenge reaches the client" "$(header www-authenticate)" 'Bearer error="invalid_token"'
  check_not "$n: no redirect" "$(header location)" "http"
  check_not "$n: no Set-Cookie" "$(header set-cookie)" "="
  check "$n: problem+json" "$(header content-type)" application/problem+json
  check_not "$n: no login_url" "$(cat "$WORK/b")" login_url
done
for n in basic empty; do
  code=$(bcall $n "$P/api/v1/me"); check "$n -> 401" "$code" 401
  [[ "$(header www-authenticate)" == "Bearer" ]] && pass "$n: bare Bearer challenge, no error" || fail "$n: challenge is '$(header www-authenticate)'"
  check_not "$n: no redirect" "$(header location)" "http"
done
code=$(nav "$WORK/tok-none" -H @"$(tokfile malformed)" "$P/"); check "navigation with a bad Bearer is a 401, not a login redirect" "$code" 401

echo "  -- (c) valid cookie + invalid Authorization on the portal host: no fallback to the cookie"
code=$(api "$JTA" "$P/api/v1/me"); check "control: the cookie alone works" "$code" 200
code=$(api "$JTA" -H @"$(tokfile malformed)" "$P/api/v1/me"); check "cookie + malformed Bearer -> 401" "$code" 401
check "challenge" "$(header www-authenticate)" 'Bearer error="invalid_token"'
code=$(api "$JTA" -H @"$(tokfile basic)" "$P/api/v1/me"); check "cookie + Basic -> 401" "$code" 401
code=$(api "$JTA" -H @"$(tokfile unknown)" "$P/api/v1/me"); check "cookie + unknown Bearer -> 401" "$code" 401

echo "  -- (b) token ignored on another host, and the app keeps its own Authorization"
code=$(api "$WORK/tok-none" -H @"$(tokfile alice-read)" "$A/api/whoami"); check "valid token without cookie on hello-a -> 401 (session path decides)" "$code" 401
check "login_url as for any unauthenticated request" "$(cat "$WORK/b")" login_url
check_not "no Bearer challenge on an app host" "$(header www-authenticate)" Bearer
code=$(nav "$WORK/tok-none" -H @"$(tokfile alice-read)" "$A/"); check "navigation with a valid token on hello-a -> login redirect" "$code" 302
code=$(api "$JTH" -H 'Authorization: Bearer garbage' "$A/api/whoami"); check "valid cookie + garbage Authorization on hello-a -> 200" "$code" 200
code=$(api "$JTH" -H @"$(tokfile alice-read)" "$A/__smoke_whoami"); check "valid cookie + token on hello-a -> 200" "$code" 200
if [[ "$(grep -i '^Authorization:' "$WORK/b" | tr -d '\r')" == "$(tr -d '\r\n' < "$(tokfile alice-read)")" ]]; then pass "hello-a still receives its own Authorization header, unchanged"; else fail "hello-a did not receive the Authorization header it was sent"; fi
check "assertion issued for the app (session path)" "$(body_clean)" "Platform-Assertion: ey"

echo "  -- (g) the secret never reaches the portal: fixture with the portal host rule and the portal middleware list"
code=$(bcall alice-read "$P/__smoke_whoami"); check "fixture reached with a token" "$code" 200
check "request really got through the portal host rule" "$(body_clean)" "Host: $P_HOST"
check "assertion arrives (ForwardAuth accepted the token)" "$(body_clean)" "Platform-Assertion: ey"
check_not "no Authorization header at the portal side" "$(tr 'A-Z' 'a-z' < "$WORK/b")" "authorization"
if leaks "$WORK/b"; then fail "a Bearer value is in the portal fixture echo"; else pass "the secret is not in the echo"; fi
code=$(bcall robot "$P/__smoke_whoami"); check "fixture reached with the robot token" "$code" 200
check_not "no Authorization header at the portal side (robot)" "$(tr 'A-Z' 'a-z' < "$WORK/b")" "authorization"
if leaks "$WORK/b"; then fail "a Bearer value is in the portal fixture echo (robot)"; else pass "the robot secret is not in the echo"; fi
code=$(api "$JTA" -H 'Authorization: Bearer garbage' "$P/__smoke_whoami"); check "cookie + garbage Authorization is refused before the fixture" "$code" 401

echo "  -- a token denied by the app rule is recorded in access_denials (user = token owner, no sid)"
# The portal app is the only app a token reaches, and the portal rule allows any active user, so the denial needs the app
# disabled for a moment (SQL as the owner: the API guards refuse it; the trap re-enables it). The trigger NOTIFYs the decision
# caches, so the next request is a cache miss and writes the row (one per miss: assert on existence since the section start).
if $K -n platform-db exec postgres-0 -- psql -q -U platform_owner -d platform -c "UPDATE apps SET status = 'disabled' WHERE id = '$PORTAL_APP_ID'" >/dev/null 2>&1; then
  code=000; t0=$(now_ms)
  while (( $(now_ms) - t0 < REVOKE_DEADLINE_MS )); do code=$(bcall robot "$P/api/v1/me"); [[ "$code" == 403 ]] && break; sleep 0.2; done
  restore_portal_app
  check "portal app is active again after the check" "$($K -n platform-db exec postgres-0 -- psql -At -q -U platform_owner -d platform -c "SELECT status FROM apps WHERE id = '$PORTAL_APP_ID'" 2>&1)" active
  check "robot token with the portal app disabled -> 403" "$code" 403
  check "access_denials row: user = the robot, sid NULL, portal host, app_disabled" "$($K -n platform-db exec postgres-0 -- psql -At -q -U platform_owner -d platform -c \
    "SELECT CASE WHEN count(*) > 0 THEN 'denied' ELSE 'none' END FROM access_denials WHERE user_id = '$RID' AND sid IS NULL AND host = '$P_HOST' AND reason = 'app_disabled' AND occurred_at >= '$SINCE_22'")" denied
  code=000; t0=$(now_ms)
  while (( $(now_ms) - t0 < REVOKE_DEADLINE_MS )); do code=$(bcall robot "$P/api/v1/me"); [[ "$code" == 200 ]] && break; sleep 0.2; done
  check "robot token works again once the app is re-enabled" "$code" 200
else
  fail "could not disable the portal app by SQL"
fi

echo "  -- revocation: the very next request fails (no cache on the token row)"
code=$(bcall alice-del "$P/api/v1/me"); check "before revoke (DELETE /me/tokens/{id})" "$code" 200
code=$(mutate "$JTA" DELETE "$P/api/v1/me/tokens/$(tid alice-del)" same-origin application/json); check "owner revokes the token" "$code" 204
code=$(bcall alice-del "$P/api/v1/me"); check "next request -> 401" "$code" 401
check "revoked challenge" "$(header www-authenticate)" 'Bearer error="invalid_token"'
code=$(api "$JTA" -H @"$(tokfile alice-del)" "$P/api/v1/me"); check "cookie + revoked token -> 401" "$code" 401
code=$(bcall alice-sec "$P/api/v1/me"); check "before revoke by secret" "$code" 200
printf '{"token":"%s"}' "$(cut -d' ' -f3 "$(tokfile alice-sec)")" > "$WORK/revoke.json"
code=$(api "$JTM" -X POST -H "Origin: $P" -H 'Sec-Fetch-Site: same-origin' -H 'Content-Type: application/json' --data-binary @"$WORK/revoke.json" "$P/api/v1/tokens/revoke")
check "platform admin revokes by secret" "$code" 204
code=$(bcall alice-sec "$P/api/v1/me"); check "next request -> 401" "$code" 401
bad=0; for _ in $(seq 1 10); do [[ "$(bcall alice-sec "$P/api/v1/me")" == 401 ]] || bad=$((bad + 1)); done
if (( bad == 0 )); then pass "10/10 following requests 401"; else fail "$bad of 10 following requests were not 401"; fi
code=$(api "$JTM" "$P/api/v1/robots?status=active&limit=200"); check "GET /robots?status=active&limit=200" "$code" 200
check "  (lists smoke-robot while active)" "$(json_get "$WORK/b" '"'"$RID"'" in [r["id"] for r in d["robots"]]')" True
code=$(mutate "$JTM" PATCH "$P/api/v1/robots/$RID" same-origin application/json '{"status":"disabled"}'); check "admin disables the robot" "$code" 200
code=$(api "$JTM" "$P/api/v1/robots?status=disabled&limit=200"); check "GET /robots?status=disabled&limit=200" "$code" 200
check "  (only disabled robots)" "$(json_get "$WORK/b" 'all(r["status"] == "disabled" for r in d["robots"])')" True
check "  (includes smoke-robot)" "$(json_get "$WORK/b" '"'"$RID"'" in [r["id"] for r in d["robots"]]')" True
code=$(api "$JTM" "$P/api/v1/robots?status=active&limit=200"); check "GET /robots?status=active after the disable" "$code" 200
check "  (only active robots)" "$(json_get "$WORK/b" 'all(r["status"] == "active" for r in d["robots"])')" True
check "  (no longer lists smoke-robot)" "$(json_get "$WORK/b" '"'"$RID"'" in [r["id"] for r in d["robots"]]')" False
code=$(api "$JTM" "$P/api/v1/robots?status=bogus"); check "GET /robots?status=bogus" "$code" 422
code=$(bcall robot "$P/api/v1/me"); check "robot token dies with the robot -> 401" "$code" 401
code=$(bcall alice-read "$P/api/v1/me"); check "the other tokens are untouched" "$code" 200

echo "  -- last_used_at is set after the throttled flush (once a minute per replica)"
idA="$(tid alice-read)"; end=$(( $(date +%s) + LAST_USED_DEADLINE_S )); lu=""
while (( $(date +%s) < end )); do
  api "$JTA" "$P/api/v1/me/tokens" >/dev/null
  lu="$(json_get "$WORK/b" '[t["last_used_at"] for t in d["tokens"] if t["id"] == "'"$idA"'"][0] or ""')"
  [[ -n "$lu" ]] && break; sleep 5
done
if [[ -n "$lu" ]]; then pass "last_used_at of the used token is set ($lu)"; else fail "last_used_at still empty after ${LAST_USED_DEADLINE_S}s"; fi
api "$JTM" "$P/api/v1/robots/$RID/tokens" >/dev/null
check "robot token last_used_at is set too" "$(json_get "$WORK/b" '[t["last_used_at"] is not None for t in d["tokens"] if t["name"] == "smoke-robot"][0]')" True

echo "  -- (f) no Bearer value in any log (read from the start of this section; a log that cannot be read or holds none of its traffic fails)"
sleep 2
readlog() { # readlog <file> <label> <kubectl logs args...>
  local f="$1" label="$2"; shift 2
  if $K logs "$@" --since-time="$SINCE_22" > "$WORK/$f.log" 2>/dev/null; then pass "$label log read ($(wc -l < "$WORK/$f.log") lines)"; else fail "kubectl logs failed for $label"; fi
}
readlog tr Traefik -n traefik deploy/traefik
readlog pa platform-api -n platform -l app.kubernetes.io/name=platform-api --prefix=false --tail=-1
readlog pt portal -n portal -l app.kubernetes.io/name=portal -c portal --prefix=false --tail=-1
# Traffic of this section must be in each log, or "no secret found" proves nothing. The portal does not log requests: its log
# only proves the process was read (probe rounds), the secret scan is what matters there.
grep -q '/__smoke_whoami' "$WORK/tr.log" && pass "Traefik log holds this section's requests (/__smoke_whoami)" || fail "Traefik log lacks the section's requests"
grep -q 'token_' "$WORK/pa.log" && pass "platform-api log holds token decisions of this section" || fail "platform-api log lacks token decisions"
[[ -s "$WORK/pt.log" ]] && pass "portal log is not empty" || fail "portal log is empty"
for l in tr:Traefik pa:platform-api pt:portal; do
  if leaks "$WORK/${l%%:*}.log"; then fail "a Bearer value sent by this section is in the ${l#*:} logs"; else pass "no Bearer value in the ${l#*:} logs"; fi
  if grep -q 'ptk_' "$WORK/${l%%:*}.log"; then fail "a 'ptk_' string is in the ${l#*:} logs"; else pass "no 'ptk_' string in the ${l#*:} logs"; fi
done
for r in token_malformed token_unknown token_revoked token_no_bearer token_scope; do
  if grep -q "$r" "$WORK/pa.log"; then pass "decision log carries reason $r"; else fail "decision log lacks reason $r"; fi
done

echo "  -- cleanup: remaining token revoked, fixtures removed (the robot stays, disabled)"
code=$(mutate "$JTA" DELETE "$P/api/v1/me/tokens/$idA" same-origin application/json); check "last token revoked" "$code" 204
$K delete -f "$REL/smoke-whoami.yaml" -f "$REL/smoke-whoami-portal.yaml" --wait=false >/dev/null

section "23. portalctl (contract block 6, part 5): grant and revoke through the API with a token"
# The host reaches the portal like curl does: public DNS -> 127.0.0.1 and the real Let's Encrypt wildcard, so no
# --resolve and no SSL_CERT_FILE are needed (the CLI uses the system roots).
CTL="$ROOT/.tools/portalctl/portalctl"
rm -f "$CTL" # a broken build must never run against a binary left by a previous run
if (cd platform/api && ../../gk env CGO_ENABLED=0 go build -o ../../.tools/portalctl/portalctl ./cmd/portalctl) >"$WORK/ctl-build.log" 2>&1; then
  pass "portalctl built (static, under .tools/portalctl)"
else
  fail "portalctl build failed: $(tail -n 5 "$WORK/ctl-build.log")"
fi
if [[ -x "$CTL" ]]; then
  CTL_SUBJECT=carol@example.com # seeded, holds no grant on demo
  : > "$WORK/ctl-all.out"
  # ctl <token name> <args...>: runs the CLI with that token from its Authorization file; output in $WORK/ctl.out (and
  # appended to ctl-all.out, scanned for the secrets at the end), exit code in $CTL_RC.
  ctl() {
    { set +x; } 2>/dev/null
    local n="$1"; shift
    PORTAL_URL="$P" PORTAL_API_TOKEN="$(cut -d' ' -f3 "$(tokfile "$n")")" "$CTL" "$@" > "$WORK/ctl.out" 2>&1; CTL_RC=$?
    cat "$WORK/ctl.out" >> "$WORK/ctl-all.out"
  }
  # demo_viewer <email|robot id>: whether that user holds a direct viewer grant on demo, read through the admin session.
  demo_viewer() {
    api "$JTM" "$P/api/v1/initiatives/$DEMO_ID/grants" >/dev/null
    json_get "$WORK/b" 'any(g["user"] and g["role"]["key"] == "viewer" and "'"$1"'" in (g["user"]["id"], g["user"]["email"]) for g in d["grants"])'
  }
  # drop_ctl_grants: removes carol's and the robot's direct viewer grants on demo through the admin session (not through the CLI under test).
  drop_ctl_grants() {
    api "$JTM" "$P/api/v1/initiatives/$DEMO_ID/grants" >/dev/null
    local g
    for g in $(json_get "$WORK/b" '" ".join(g["id"] for g in d["grants"] if g["user"] and g["role"]["key"] == "viewer" and (g["user"]["id"] == "'"$RID"'" or g["user"]["email"] == "'"$CTL_SUBJECT"'"))'); do
      mutate "$JTM" DELETE "$P/api/v1/grants/$g" same-origin application/json >/dev/null
    done
  }
  # Re-runnable: drop leftovers of an interrupted run, and bring the robot back (section 22 disabled it).
  code=$(mutate "$JTM" PATCH "$P/api/v1/robots/$RID" same-origin application/json '{"status":"active"}'); check "smoke-robot active again" "$code" 200
  mk_token "$JTM" "$P/api/v1/me/tokens" ctl-admin admin
  mk_token "$JTM" "$P/api/v1/me/tokens" ctl-read read
  drop_ctl_grants

  echo "  -- grant and revoke by email"
  ctl ctl-admin grant "$CTL_SUBJECT" demo viewer; check "grant exits 0" "$CTL_RC" 0
  check "first grant changed" "$(head -n 1 "$WORK/ctl.out")" changed
  check "grant done line" "$(cat "$WORK/ctl.out")" "grant done: $CTL_SUBJECT -> demo/viewer"
  check "the grant is visible through the API" "$(demo_viewer "$CTL_SUBJECT")" True
  ctl ctl-admin grant "$CTL_SUBJECT" demo viewer; check "second grant exits 0" "$CTL_RC" 0
  check "second grant unchanged" "$(head -n 1 "$WORK/ctl.out")" unchanged
  ctl ctl-admin revoke "$CTL_SUBJECT" demo viewer; check "revoke exits 0" "$CTL_RC" 0
  check "first revoke changed" "$(head -n 1 "$WORK/ctl.out")" changed
  check "revoke done line" "$(cat "$WORK/ctl.out")" "revoke done: $CTL_SUBJECT -> demo/viewer"
  check "the grant is gone through the API" "$(demo_viewer "$CTL_SUBJECT")" False
  ctl ctl-admin revoke "$CTL_SUBJECT" demo viewer; check "second revoke exits 0" "$CTL_RC" 0
  check "second revoke unchanged" "$(head -n 1 "$WORK/ctl.out")" unchanged

  echo "  -- robot subject"
  ctl ctl-admin grant robot:smoke-robot demo viewer; check "robot grant exits 0" "$CTL_RC" 0
  check "robot grant changed" "$(head -n 1 "$WORK/ctl.out")" changed
  check "robot grant done line" "$(cat "$WORK/ctl.out")" "grant done: robot:smoke-robot -> demo/viewer"
  check "the robot grant is visible through the API" "$(demo_viewer "$RID")" True
  ctl ctl-admin grant robot:smoke-robot demo viewer; check "robot grant again: unchanged" "$(head -n 1 "$WORK/ctl.out")" unchanged
  ctl ctl-admin revoke robot:smoke-robot demo viewer; check "robot revoke exits 0" "$CTL_RC" 0
  check "robot revoke changed" "$(head -n 1 "$WORK/ctl.out")" changed
  check "the robot grant is gone through the API" "$(demo_viewer "$RID")" False
  ctl ctl-admin revoke robot:smoke-robot demo viewer; check "robot revoke again: unchanged" "$(head -n 1 "$WORK/ctl.out")" unchanged

  echo "  -- failures"
  ctl ctl-read grant "$CTL_SUBJECT" demo viewer; check "read-scope token: write fails with exit 1" "$CTL_RC" 1
  check "403 reported" "$(cat "$WORK/ctl.out")" "403 Forbidden"
  check "and nothing was granted" "$(demo_viewer "$CTL_SUBJECT")" False
  ctl ctl-admin grant "$CTL_SUBJECT" no-such-initiative viewer; check "unknown initiative: exit 1" "$CTL_RC" 1
  check "  (message)" "$(cat "$WORK/ctl.out")" 'unknown initiative "no-such-initiative"'
  ctl ctl-admin grant robot:no-such-robot demo viewer; check "unknown robot: exit 1" "$CTL_RC" 1
  check "  (message)" "$(cat "$WORK/ctl.out")" 'unknown robot "no-such-robot"'
  PORTAL_URL="${P/https:/http:}" PORTAL_API_TOKEN="$(cut -d' ' -f3 "$(tokfile ctl-admin)")" "$CTL" grant "$CTL_SUBJECT" demo viewer > "$WORK/ctl.out" 2>&1; CTL_RC=$?
  cat "$WORK/ctl.out" >> "$WORK/ctl-all.out"
  check "PORTAL_URL over http: exit 2" "$CTL_RC" 2
  PORTAL_URL="$P" PORTAL_API_TOKEN="" "$CTL" grant "$CTL_SUBJECT" demo viewer > "$WORK/ctl.out" 2>&1; CTL_RC=$?
  check "no token: exit 2" "$CTL_RC" 2
  PORTAL_URL="$P" PORTAL_API_TOKEN=x "$CTL" grant not-an-email demo viewer > "$WORK/ctl.out" 2>&1; CTL_RC=$?
  check "bad subject: exit 2" "$CTL_RC" 2

  echo "  -- cleanup: tokens revoked, the robot disabled (a grant to it is then refused)"
  drop_ctl_grants
  for n in ctl-admin ctl-read; do
    code=$(mutate "$JTM" DELETE "$P/api/v1/me/tokens/$(tid $n)" same-origin application/json); check "$n token revoked" "$code" 204
  done
  code=$(mutate "$JTM" PATCH "$P/api/v1/robots/$RID" same-origin application/json '{"status":"disabled"}'); check "smoke-robot disabled again" "$code" 200
  mk_token "$JTM" "$P/api/v1/me/tokens" ctl-last admin
  ctl ctl-last grant robot:smoke-robot demo viewer; check "grant to a disabled robot: exit 1" "$CTL_RC" 1
  check "  (message)" "$(cat "$WORK/ctl.out")" "is disabled"
  code=$(mutate "$JTM" DELETE "$P/api/v1/me/tokens/$(tid ctl-last)" same-origin application/json); check "ctl-last token revoked" "$code" 204
  if leaks "$WORK/ctl-all.out"; then fail "a Bearer value is in the portalctl output"; else pass "no Bearer value in any portalctl output ($(wc -l < "$WORK/ctl-all.out") lines, success and failure paths)"; fi
fi

echo
if (( FAILS == 0 )); then echo "ALL CHECKS PASSED${BROWSER_SKIPPED:+ (browser check SKIPPED)}"; else
  echo "$FAILS CHECK(S) FAILED:"
  for f in "${FAILED[@]}"; do echo "  - section $f"; done
  exit 1
fi
