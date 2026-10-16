#!/usr/bin/env bash
# End-to-end smoke of the platform in the k3d cluster, with curl from the HOST
# (*.apps.wysted.dev -> 127.0.0.1 through public DNS; Traefik serves the real wildcard certificate).
# Needs: up.sh already run, python3 on the host. Cluster access goes through ./tk.
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
cleanup() {
  $K delete pod probe-default --ignore-not-found --wait=false >/dev/null 2>&1
  $K -n hello-a delete pod probe-hello --ignore-not-found --wait=false >/dev/null 2>&1
  $K delete namespace smoke-app --ignore-not-found --wait=false >/dev/null 2>&1
  $K delete -f "$REL/smoke-whoami.yaml" --ignore-not-found --wait=false >/dev/null 2>&1
  drop_smoke_groups
  $K -n monitoring delete pod probe-prom probe-grafana probe-other --ignore-not-found --wait=false >/dev/null 2>&1
  $K -n monitoring delete networkpolicy smoke-allow-egress --ignore-not-found >/dev/null 2>&1
  rm -rf "$WORK" "$GW"
}
trap cleanup EXIT

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
# also measured per replica: a probe pod calls /forwardauth on each pod IP while the grant is deleted.
IPS="$($K -n platform get pods -l app.kubernetes.io/name=platform-api -o jsonpath='{.items[*].status.podIP}')"
COOKIE="$(awk '$1 ~ /hello-a/ && $6=="__Host-Http-platform_app"{print $7}' "$JA")"
echo "  replicas: $IPS"
$K -n hello-a exec probe-hello -- sh -c 'end=$(( $(date +%s) + 10 )); while [ $(date +%s) -lt $end ]; do for ip in '"$IPS"'; do s=$(curl -s -o /dev/null --max-time 2 -w "%{http_code}" -H "X-Forwarded-Host: '$A_HOST'" -H "X-Forwarded-Method: GET" -H "X-Forwarded-Uri: /api/whoami" -H "Accept: application/json" -H "Sec-Fetch-Mode: cors" -H "Cookie: __Host-Http-platform_app='"$COOKIE"'" http://$ip:8081/forwardauth); echo "$(date +%s) $ip $s"; done; sleep 0.1; done' > "$WORK/replica.log" 2>/dev/null &
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
    got="$(probe "$1" "$2" "$3")"; [[ "$got" == "$4" ]] && break; sleep 1
  done
  echo "$got"
}
probe_tcp() { # probe_tcp <ns> <pod> <host> <port>: connected / blocked
  if $K -n "$1" exec "$2" -- sh -c "curl -s --max-time 4 -o /dev/null telnet://$3:$4 </dev/null; [ \$? -eq 28 ]" >/dev/null 2>&1; then echo connected; else echo blocked; fi
}
check "default -> platform-db:5432" "$(probe_tcp default probe-default postgres.platform-db.svc.cluster.local 5432)" blocked
check "default -> platform-api-internal:8081" "$(probe default probe-default http://platform-api-internal.platform.svc.cluster.local:8081/healthz)" 000
check "default -> platform-api-public:8080" "$(probe default probe-default http://platform-api-public.platform.svc.cluster.local:8080/login)" 000
check "default -> hello-a directly" "$(probe default probe-default http://hello.hello-a.svc.cluster.local:8080/healthz)" 000
check "hello-a pod -> platform-db:5432 (egress)" "$(probe_tcp hello-a probe-hello postgres.platform-db.svc.cluster.local 5432)" blocked
check "hello-a pod -> hello-b" "$(probe hello-a probe-hello http://hello.hello-b.svc.cluster.local:8080/healthz)" 000
check "hello-a pod -> platform-api-public:8080 (not allowed)" "$(probe hello-a probe-hello http://platform-api-public.platform.svc.cluster.local:8080/login)" 000
echo "  -- positive controls:"
check "hello-a pod -> JWKS on platform-api-internal:8081" "$(probe hello-a probe-hello http://platform-api-internal.platform.svc.cluster.local:8081/.well-known/jwks.json)" 200
check "hello-a pod -> platform-api-internal /healthz" "$(probe hello-a probe-hello http://platform-api-internal.platform.svc.cluster.local:8081/healthz)" 200
check "platform-api reaches PostgreSQL (readyz via internal listener)" "$(probe hello-a probe-hello http://platform-api-internal.platform.svc.cluster.local:8081/readyz)" 200

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

section "15. Portal responses: security headers, no CORS, /metrics not served on the public host"
code=$(api "$JAD" -H 'Origin: https://evil.example' "$P/api/v1/me"); check "GET with a foreign Origin" "$code" 200
hdrs="$(tr -d '\r' < "$WORK/h" | tr A-Z a-z)"
for h in "content-security-policy: default-src 'none'; frame-ancestors 'none'" "strict-transport-security: max-age=63072000; includesubdomains" \
         "x-frame-options: deny" "cross-origin-opener-policy: same-origin" "cross-origin-resource-policy: same-origin" \
         "referrer-policy: no-referrer" "x-content-type-options: nosniff"; do check "header" "$hdrs" "$h"; done
check_not "no Access-Control-* on the response" "$hdrs" "access-control-"
code=$(curl -s -o "$WORK/b" -D "$WORK/h" -w '%{http_code}' -X OPTIONS -H "Origin: https://evil.example" -H 'Access-Control-Request-Method: POST' "$P/api/v1/groups")
echo "  preflight status: $code"; check_not "no Access-Control-* on a preflight" "$(tr -d '\r' < "$WORK/h" | tr A-Z a-z)" "access-control-"
code=$(api "$JAD" "$P/metrics"); check "authenticated /metrics on the public host" "$code" 404
code=$(curl -s -o "$WORK/b" -w '%{http_code}' -H 'Accept: application/json' "$P/metrics"); check "anonymous /metrics on the public host (ForwardAuth, API request)" "$code" 401
for path in /healthz /readyz; do
  code=$(api "$JAD" "$P$path"); check "authenticated $path on the public host" "$code" 404
done

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
check "hello-a: JWKS on platform-api-internal:8081 (positive control)" "$(wait_probe hello-a probe-hello http://platform-api-internal.platform.svc.cluster.local:8081/.well-known/jwks.json 200)" 200
check "smoke-app: JWKS on platform-api-internal:8081 (positive control)" "$(wait_probe smoke-app probe-open http://platform-api-internal.platform.svc.cluster.local:8081/.well-known/jwks.json 200)" 200
for pod in $PA_PODS; do
  ip="$(pod_ip platform "$pod")"
  # The replacement replica of section 12 may not be in kube-router's allow set yet: wait until its JWKS answers.
  check "smoke-app: platform-api $pod :8081 JWKS by pod IP (positive control)" "$(wait_probe smoke-app probe-open http://$ip:8081/.well-known/jwks.json 200)" 200
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
check "platform-api reaches PostgreSQL (readyz, positive control)" "$(probe smoke-app probe-open http://platform-api-internal.platform.svc.cluster.local:8081/readyz)" 200
check "probe_tcp sees an open port (positive control: platform-api-internal:8081)" "$(probe_tcp smoke-app probe-open platform-api-internal.platform.svc.cluster.local 8081)" connected
check "smoke-app (app namespace) -> postgres:5432" "$(probe_tcp smoke-app probe-open postgres.platform-db.svc.cluster.local 5432)" blocked

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

echo "  -- Grafana (port-forward inside the toolbox, polled until it answers; admin password from .tools/secrets via a 0600 curl config)"
umask 077; printf 'user = "admin:%s"\n' "$(<.tools/secrets/k3d-grafana-admin-password)" > "$GW/curlrc"; umask 022
$K -n monitoring rollout status deploy/monitoring-grafana --timeout=120s >/dev/null
./tk sh -c 'cd /workspace/.tools/tmp/'"$(basename "$GW")"' && rm -f health.json dash.json ds.json anon.code
  kubectl -n monitoring port-forward deploy/monitoring-grafana 13000:3000 >pf.log 2>&1 &
  pf=$!
  B=http://127.0.0.1:13000
  for i in $(seq 1 60); do curl -s -o health.json --max-time 2 "$B/api/health" && break; sleep 0.5; done
  curl -s -o /dev/null -w "%{http_code}" "$B/api/dashboards/uid/portal-platform" > anon.code
  curl -s -K curlrc -o dash.json "$B/api/dashboards/uid/portal-platform"
  curl -s -K curlrc -o ds.json "$B/api/datasources/uid/prometheus/health"
  kill $pf' >/dev/null 2>&1
check "Grafana health: database ok" "$(json_get "$GW/health.json" 'd["database"]')" ok
check "Grafana API without credentials" "$(<"$GW/anon.code")" 401
check "dashboard 'Portal platform' is provisioned" "$(json_get "$GW/dash.json" 'd["dashboard"]["title"] + " provisioned=" + str(d["meta"]["provisioned"])')" "Portal platform provisioned=True"
check "dashboard has every panel of the versioned JSON" "$(json_get "$GW/dash.json" 'len(d["dashboard"]["panels"])')" "$(json_get platform-gitops/monitoring/dashboards/portal-platform.json 'len(d["panels"])')"
check "Grafana reaches Prometheus (datasource health)" "$(json_get "$GW/ds.json" 'd["status"]')" OK

echo "  -- NetworkPolicy of the monitoring namespace. probe-other has an allow-all EGRESS policy of its own, so what it cannot reach"
echo "     is blocked by the INGRESS policy of the destination (positive controls first); probe-prom / probe-grafana wear the labels of"
echo "     the real pods and exercise their EGRESS policies"
PROM_IP="$(pod_ip monitoring prometheus-monitoring-prometheus-0)"
API_IP="$($K get endpoints kubernetes -o jsonpath='{.subsets[0].addresses[0].ip}' 2>/dev/null)"
TRAEFIK_IP="$($K -n traefik get pod -l app.kubernetes.io/name=traefik -o jsonpath='{.items[0].status.podIP}')"
echo "  prometheus=$PROM_IP kube-apiserver=$API_IP traefik pod=$TRAEFIK_IP"
# The source of every destination-ingress negative must be able to send: probe-other reaches ports that are open and
# unfiltered by any ingress policy (the API server, a Traefik pod).
check "probe-other (allow-all egress) -> kube-apiserver $API_IP:6443 (positive: it can send)" "$(probe_tcp monitoring probe-other "$API_IP" 6443)" connected
check "probe-other (allow-all egress) -> Traefik pod :8443 (positive: it can send)" "$(probe_tcp monitoring probe-other "$TRAEFIK_IP" 8443)" connected
for pod in $PA_PODS; do
  ip="$(pod_ip platform "$pod")"
  check "monitoring Prometheus-labelled pod -> platform-api $pod:9090/metrics (positive)" "$(wait_probe monitoring probe-prom http://$ip:9090/metrics 200)" 200
  check "[ingress] probe-other -> platform-api $pod:9090 (not a Prometheus pod)" "$(probe monitoring probe-other http://$ip:9090/metrics)" 000
  check "[ingress] probe-other -> platform-api $pod:8081 (internal listener: app namespaces only)" "$(probe monitoring probe-other http://$ip:8081/.well-known/jwks.json)" 000
  check "[ingress] probe-other -> platform-api $pod:8080 (public listener: Traefik only)" "$(probe monitoring probe-other http://$ip:8080/login)" 000
  check "[egress] Prometheus-labelled pod -> platform-api $pod:8081 (internal listener)" "$(probe monitoring probe-prom http://$ip:8081/.well-known/jwks.json)" 000
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

echo
if (( FAILS == 0 )); then echo "ALL CHECKS PASSED"; else
  echo "$FAILS CHECK(S) FAILED:"
  for f in "${FAILED[@]}"; do echo "  - section $f"; done
  exit 1
fi
