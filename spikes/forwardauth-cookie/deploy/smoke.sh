#!/usr/bin/env bash
# Smoke tests for the forwardauth-cookie spike, run with curl from the HOST.
# *.apps.wysted.dev resolves to 127.0.0.1 through public DNS; Traefik serves the real wildcard cert.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"
K="./tk kubectl"
AUTH=https://auth.apps.wysted.dev
APP1=https://app1.apps.wysted.dev
APP2=https://app2.apps.wysted.dev
CURL_IMG=curlimages/curl:8.11.1
WORK="$(mktemp -d)"
FAILS=0

pass() { echo "  PASS: $*"; }
fail() { echo "  FAIL: $*"; FAILS=$((FAILS + 1)); }
section() { echo; echo "== $*"; }
# check <desc> <actual> <expected substring>
check() { if [[ "$2" == *"$3"* ]]; then pass "$1 ($3)"; else fail "$1: got '$2', want '$3'"; fi; }

# req <jar> <args...>: curl with headers in $WORK/h, body in $WORK/b; prints the status code.
req() {
  local jar="$1"; shift
  curl -s -o "$WORK/b" -D "$WORK/h" -w '%{http_code}' -b "$jar" -c "$jar" "$@"
}
header() { tr -d '\r' < "$WORK/h" | awk -v k="$(echo "$1" | tr A-Z a-z)" 'tolower($1)==k":" {sub(/^[^:]*: */,""); print}'; }
enc() { python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1],safe=""))' "$1"; }

# login <jar> <user> <app base url>: runs login + callback, leaves the app cookie in <jar>.
login() {
  local jar="$1" user="$2" app="$3"
  req "$jar" "$AUTH/login?user=$user&rd=$(enc "$app/")" >/dev/null
  local cb; cb="$(header location)"
  req "$jar" -H 'Accept: text/html' "$cb" >/dev/null
}

section "1. HTML navigation without cookie -> 302 to login"
code=$(req "$WORK/j1" -H 'Accept: text/html' "$APP1/")
check "status" "$code" 302
check "location" "$(header location)" "$AUTH/login?rd=https%3A%2F%2Fapp1.apps.wysted.dev%2F"
echo "  Location: $(header location)"

section "2. JSON/API without cookie -> 401 JSON"
code=$(req "$WORK/j2" -H 'Accept: application/json' "$APP1/api/x")
check "status" "$code" 401
echo "  body: $(cat "$WORK/b")"; echo "  content-type: $(header content-type)"

section "3. CORS preflight passes to the app without token"
code=$(req "$WORK/j3" -X OPTIONS -H 'Origin: https://x.example' -H 'Access-Control-Request-Method: POST' "$APP1/api/x")
echo "  status: $code  body: $(cat "$WORK/b")"
# The app itself answers 401 (no token), but authz must NOT have produced the 401/302: check via the body.
# authz answers 401 {"error":"unauthenticated"}; the app answers its own {"error":"unauthorized"}.
check "reached the app (authz let it through)" "$(cat "$WORK/b")" unauthorized

section "4. Full flow alice -> app1 (Set-Cookie of the ForwardAuth 302 reaches the client)"
J="$WORK/alice"
code=$(req "$J" "$AUTH/login?user=alice&rd=$(enc "$APP1/")")
check "login status" "$code" 302
echo "  login Set-Cookie: $(header set-cookie | cut -c1-60)..."
cb="$(header location)"; echo "  callback: ${cb%%=*}=<code>"
check "callback target" "$cb" "$APP1/_portal/callback?code="
code=$(req "$J" -H 'Accept: text/html' "$cb")
check "callback status" "$code" 302
sc="$(header set-cookie)"
echo "  callback Set-Cookie: $sc"
check "Set-Cookie reaches client" "$sc" "__Host-portal_app="
check "Set-Cookie attrs" "$sc" "HttpOnly"
check "Location back to rd" "$(header location)" "$APP1/"
code=$(req "$J" -H 'Accept: application/json' "$APP1/")
check "app1 with cookie" "$code" 200
echo "  body: $(cat "$WORK/b")"
check "sub" "$(cat "$WORK/b")" 6f1c1a0e-3b7e-4c1a-9d57-0a1f6e2b7c01

section "5. Authorization per app"
code=$(req "$J" -H 'Accept: application/json' "$APP2/")
check "alice on app2 (no app2 cookie) is rejected" "$code" 401
# alice has no access to app2: even a full login flow ends in 403.
J2="$WORK/alice2"; login "$J2" alice "$APP2"
code=$(req "$J2" -H 'Accept: application/json' "$APP2/")
check "alice on app2 after login" "$code" 403
JB="$WORK/bob"; login "$JB" bob "$APP2"
code=$(req "$JB" -H 'Accept: application/json' "$APP2/")
check "bob on app2" "$code" 200; echo "  body: $(cat "$WORK/b")"
JB1="$WORK/bob1"; login "$JB1" bob "$APP1"
code=$(req "$JB1" -H 'Accept: application/json' "$APP1/")
check "bob on app1" "$code" 200
# Replay bob's app1 cookie against app2 (cookies are per host, so copy the value by hand).
v=$(awk '$6=="__Host-portal_app"{print $7}' "$JB1")
code=$(req "$WORK/none" -H 'Accept: application/json' -H "Cookie: __Host-portal_app=$v" "$APP2/")
check "bob's app1 cookie on app2 rejected" "$code" 401

section "6. One-time code cannot be reused"
req "$WORK/c" "$AUTH/login?user=bob&rd=$(enc "$APP1/")" >/dev/null; cb="$(header location)"
code=$(req "$WORK/c1" -H 'Accept: text/html' "$cb"); check "first use" "$code" 302
code=$(req "$WORK/c2" -H 'Accept: text/html' "$cb"); check "second use" "$code" 403
echo "  body: $(cat "$WORK/b")"

section "7. Forged X-Portal-Token"
code=$(req "$J" -H 'Accept: application/json' -H 'X-Portal-Token: forged.jwt.value' "$APP1/")
check "valid cookie + forged token: app got the authz token" "$code" 200
echo "  body: $(cat "$WORK/b")"
code=$(req "$WORK/n7" -H 'Accept: application/json' -H 'X-Portal-Token: forged.jwt.value' "$APP1/")
check "no cookie + forged token: stopped at authz" "$code" 401
echo "  body: $(cat "$WORK/b")"

section "8. Spoofed X-Forwarded-Host"
code=$(req "$J" -H 'Accept: application/json' -H 'X-Forwarded-Host: app2.apps.wysted.dev' "$APP1/")
check "alice on app1 with XFH=app2 still app1 (200, not 403)" "$code" 200
check "app id" "$(cat "$WORK/b")" '"app":"app1"'
code=$(req "$WORK/n8" -H 'Accept: text/html' -H 'X-Forwarded-Host: app2.apps.wysted.dev' "$APP1/")
check "rd keeps the real host" "$(header location)" "app1.apps.wysted.dev"

section "Auth host stays reachable and never gets a token"
code=$(req "$WORK/a" "$AUTH/.well-known/jwks.json"); check "jwks status" "$code" 200
check "no X-Portal-Token on auth host" "$(header x-portal-token)" ""

section "10. NetworkPolicy enforcement"
# GOTCHA: kube-router applies policy to a NEW pod a few seconds after it is Ready, so probing
# from a fresh `kubectl run` pod gives false results. Use long-lived probe pods and wait for the sync.
PROBE_PODS=(default spike-app2)
cleanup_probes() {
  for ns in "${PROBE_PODS[@]}"; do $K -n "$ns" delete pod netprobe --ignore-not-found --wait=false >/dev/null 2>&1; done
}
trap 'cleanup_probes; rm -rf "$WORK"' EXIT
for ns in "${PROBE_PODS[@]}"; do
  $K -n "$ns" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: netprobe}
spec:
  automountServiceAccountToken: false
  securityContext: {runAsNonRoot: true, runAsUser: 100, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: c
      image: $CURL_IMG
      imagePullPolicy: IfNotPresent
      command: [sleep, "600"]
      securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}}
YAML
  $K -n "$ns" wait --for=condition=Ready pod/netprobe --timeout=90s >/dev/null
done
sleep 15
# probe <ns> <url> -> prints the HTTP status, or 'blocked' when the connection fails
probe() {
  local out
  out=$($K -n "$1" exec netprobe -- curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$2" 2>/dev/null </dev/null)
  if [[ -z "$out" || "$out" == 000* ]]; then echo blocked; else echo "$out"; fi
}
r=$(probe default http://app.spike-app1.svc.cluster.local:8080/); check "ns default -> app1 direct" "$r" blocked
r=$(probe spike-app2 http://app.spike-app1.svc.cluster.local:8080/); check "spike-app2 -> app1 direct" "$r" blocked
r=$(probe default http://authz.spike-authz.svc.cluster.local:8080/.well-known/jwks.json); check "ns default -> authz" "$r" blocked
r=$(probe spike-app2 http://authz.spike-authz.svc.cluster.local:8080/.well-known/jwks.json); check "spike-app2 -> authz JWKS (allowed)" "$r" 200
r=$(probe spike-app2 https://example.com/); check "spike-app2 -> internet" "$r" blocked

section "11. No ServiceAccount token mounted"
for ns in spike-authz spike-app1 spike-app2; do
  spec=$($K -n "$ns" get pods -o jsonpath='{range .items[*]}{.spec.automountServiceAccountToken}{"|"}{.spec.volumes}{"|"}{.spec.containers[*].volumeMounts}{"\n"}{end}')
  check "$ns pod spec (automount=false, no volumes/mounts)" "$spec" "false||"
done

section "12. A: authRequestHeaders allowlist (header-logging auth server via probe-forwardauth.sh)"
PROBE_LOG="$WORK/probe.log"
bash "$ROOT/spikes/forwardauth-cookie/deploy/probe-forwardauth.sh" > "$PROBE_LOG" 2>&1
# Wait for the real Middleware to be live again before the next sections.
sleep 5
line() { rg "REQ GET /forwardauth" "$PROBE_LOG" | rg -F "'X-Forwarded-Uri': '$1'" | head -1; }
extra="$(line /p-extra)"
[[ -n "$extra" ]] || fail "no /p-extra line in the probe log"
echo "  /p-extra request seen by the auth server:"; echo "    $extra"
for h in X-Forwarded-Method X-Forwarded-Host X-Forwarded-Uri X-Forwarded-Proto X-Forwarded-For; do
  check "$h still reaches the auth server" "$extra" "'$h':"
done
for h in Authorization X-Custom; do
  if [[ "$extra" == *"'$h':"* || -z "$extra" ]]; then fail "$h (outside the allowlist) reached the auth server"; else pass "$h does NOT reach the auth server"; fi
done
check "client XFH ignored (real host kept)" "$extra" "'X-Forwarded-Host': 'app1.apps.wysted.dev'"
get_line="$(line '/p-get?a=1')"
check "allowlisted Cookie passes" "$get_line" "'Cookie': '__Host-portal_app=PROBE'"
check "allowlisted Origin passes" "$get_line" "'Origin': 'https://x.example'"
check "allowlisted Sec-Fetch-Mode passes" "$get_line" "'Sec-Fetch-Mode': 'navigate'"
check "allowlisted User-Agent passes" "$get_line" "'User-Agent':"
check "preflight ACRM passes" "$(line /p-options)" "'Access-Control-Request-Method': 'POST'"

section "13. B: X-Portal-Token strip (entrypoint headers middleware before ForwardAuth)"
if [[ "$extra" == *"'X-Portal-Token':"* ]]; then fail "forged X-Portal-Token reached authz (app host)"; else pass "forged X-Portal-Token does not reach authz (app host)"; fi
authhost="$(line /p-authhost)"; echo "  /p-authhost: $authhost"
check "auth host request reached authz" "$authhost" "'X-Forwarded-Uri': '/p-authhost'"
if [[ "$authhost" == *"'X-Portal-Token':"* ]]; then fail "forged X-Portal-Token reached authz (auth host)"; else pass "forged X-Portal-Token does not reach authz (auth host)"; fi
pre="$(line /p-preflight)"
if [[ -n "$pre" && "$pre" != *"'X-Portal-Token':"* ]]; then pass "forged token not sent to authz on preflight"; else fail "preflight line: '$pre'"; fi

# B1 needs to SEE what the app receives: temporary traefik/whoami in spike-app1, reached through a
# longer-rule IngressRoute on the SAME host (authz only knows app1/app2/auth hosts), so authz and the
# entrypoint chain behave exactly as for the real app. Everything is labelled and removed at the end.
cleanup_whoami() {
  $K -n spike-app1 delete pod,svc,networkpolicy,ingressroute -l portal-spike/temp=whoami --ignore-not-found >/dev/null 2>&1
}
trap 'cleanup_probes; cleanup_whoami; rm -rf "$WORK"' EXIT
$K apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: whoami, namespace: spike-app1, labels: {app: whoami, portal-spike/temp: whoami}}
spec:
  automountServiceAccountToken: false
  securityContext: {runAsNonRoot: true, runAsUser: 65532, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: whoami
      image: traefik/whoami:v1.10.3
      imagePullPolicy: IfNotPresent
      args: [--port=8080]
      ports: [{containerPort: 8080}]
      resources: {requests: {cpu: 5m, memory: 8Mi}, limits: {cpu: 100m, memory: 32Mi}}
      securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
---
apiVersion: v1
kind: Service
metadata: {name: whoami, namespace: spike-app1, labels: {portal-spike/temp: whoami}}
spec: {selector: {app: whoami}, ports: [{port: 8080, targetPort: 8080}]}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: whoami-allow-ingress-traefik, namespace: spike-app1, labels: {portal-spike/temp: whoami}}
spec:
  podSelector: {matchLabels: {app: whoami}}
  policyTypes: [Ingress]
  ingress:
    - from: [{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: traefik}}}]
      ports: [{protocol: TCP, port: 8080}]
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata: {name: whoami, namespace: spike-app1, labels: {portal-spike/temp: whoami}}
spec:
  entryPoints: [websecure]
  routes:
    - kind: Rule
      match: "Host(\`app1.apps.wysted.dev\`) && PathPrefix(\`/whoami-probe\`)"
      services: [{name: whoami, port: 8080}]
  tls: {}
YAML
$K -n spike-app1 wait --for=condition=Ready pod/whoami --timeout=120s >/dev/null
sleep 15
code=$(req "$WORK/w1" -X OPTIONS -H 'Origin: https://x.example' -H 'Access-Control-Request-Method: POST' -H 'X-Portal-Token: forged' -H 'Authorization: Bearer keepme' "$APP1/whoami-probe/x")
echo "  preflight status: $code"; sed 's/^/    /' "$WORK/b" | head -20
check "preflight reached the temp app (authz 200 without token)" "$code" 200
check "temp app saw the request" "$(cat "$WORK/b")" "X-Forwarded-Host: app1.apps.wysted.dev"
if rg -qi '^X-Portal-Token:' "$WORK/b"; then fail "app received the forged X-Portal-Token on preflight"; else pass "app does NOT receive the forged X-Portal-Token on preflight"; fi
check "other client headers untouched (Authorization reaches the app)" "$(cat "$WORK/b")" "Authorization: Bearer keepme"
# Same path on the auth host is not routed to whoami; authz-minted token on authorized requests is covered in 14.
cleanup_whoami

section "14. B3 regression: valid cookie + forged token -> the app gets the authz token"
code=$(req "$J" -H 'Accept: application/json' -H 'X-Portal-Token: forged.jwt.value' "$APP1/")
check "status" "$code" 200
check "app validated a real token (sub of alice)" "$(cat "$WORK/b")" 6f1c1a0e-3b7e-4c1a-9d57-0a1f6e2b7c01
echo "  body: $(cat "$WORK/b")"

echo
if [[ $FAILS -eq 0 ]]; then echo "ALL CHECKS PASSED"; else echo "$FAILS CHECK(S) FAILED"; exit 1; fi
