#!/usr/bin/env bash
# Temporarily points the ForwardAuth Middleware at a header-logging server (python http.server)
# to record the exact method/headers Traefik sends to the auth server. Restores the real
# Middleware on exit. While it runs, every route behaves as "authorized" (echo answers 200).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"
K="./tk kubectl"
NS=spike-authz
IMG=python:3.13-alpine

restore() {
  echo "==> restoring the real Middleware"
  $K apply -f spikes/forwardauth-cookie/deploy/authz/middleware.yaml >/dev/null
  $K -n $NS delete pod/echo svc/echo --ignore-not-found >/dev/null
}
trap restore EXIT

# The image is pulled by the node itself (k3d image import of this multi-arch image is flaky).
$K apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: echo
  namespace: $NS
  # Same label as authz so the existing NetworkPolicies admit Traefik.
  labels: {app.kubernetes.io/name: authz}
spec:
  automountServiceAccountToken: false
  securityContext: {runAsNonRoot: true, runAsUser: 65532, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: echo
      image: $IMG
      imagePullPolicy: IfNotPresent
      command: ["python", "-u", "-c"]
      args:
        - |
          import http.server
          class H(http.server.BaseHTTPRequestHandler):
              def handle_one_request(self):
                  super().handle_one_request()
              def do_ANY(self):
                  print("REQ", self.command, self.path, dict(self.headers), flush=True)
                  self.send_response(200); self.send_header("Content-Length", "0"); self.end_headers()
              do_GET = do_POST = do_OPTIONS = do_DELETE = do_PUT = do_HEAD = do_ANY
              def log_message(self, *a): pass
          http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()
      ports: [{containerPort: 8080}]
      resources: {requests: {cpu: 5m, memory: 8Mi}, limits: {cpu: 100m, memory: 32Mi}}
      securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
---
apiVersion: v1
kind: Service
metadata: {name: echo, namespace: $NS}
spec:
  selector: {app.kubernetes.io/name: authz}
  ports: [{port: 8080, targetPort: 8080}]
YAML
$K -n $NS wait --for=condition=Ready pod/echo --timeout=60s
# Service selector also matches the real authz pod; pin to the echo pod only.
$K -n $NS patch svc echo --type merge -p '{"spec":{"selector":{"run":"echo"}}}' >/dev/null
$K -n $NS label pod echo run=echo >/dev/null
$K apply -f - >/dev/null <<YAML
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: {name: forwardauth, namespace: $NS}
spec:
  forwardAuth:
    address: http://echo.$NS.svc.cluster.local:8080/forwardauth
    trustForwardHeader: false
    authRequestHeaders: [Cookie, Accept, Sec-Fetch-Mode, Origin, Access-Control-Request-Method, Access-Control-Request-Headers, X-Requested-With, User-Agent]
    maxResponseBodySize: 4096
    authResponseHeaders: [X-Portal-Token]
YAML
sleep 8

H=https://app1.apps.wysted.dev
echo "==> sending probes"
curl -sk -o /dev/null -X GET -H 'Accept: text/html' -H 'Sec-Fetch-Mode: navigate' -H 'Cookie: __Host-portal_app=PROBE' -H 'Origin: https://x.example' "$H/p-get?a=1"
curl -sk -o /dev/null -X OPTIONS -H 'Origin: https://x.example' -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: content-type' "$H/p-options"
curl -sk -o /dev/null -X POST -H 'Accept: application/json' -H 'Cookie: __Host-portal_app=PROBE' -d 'x=1' "$H/p-post"
curl -sk -o /dev/null -X DELETE "$H/p-delete"
curl -sk -o /dev/null -X GET -H "Authorization: Bearer x" -H "X-Custom: y" -H "X-Portal-Token: forged" -H "X-Forwarded-Host: evil.example" "$H/p-extra"
# Auth host: the forged assertion header must not reach authz either.
curl -sk -o /dev/null -X GET -H "X-Portal-Token: forged" "https://auth.apps.wysted.dev/p-authhost"
curl -sk -o /dev/null -X OPTIONS -H "X-Portal-Token: forged" -H "Origin: https://x.example" -H "Access-Control-Request-Method: POST" "$H/p-preflight"
sleep 2
echo "==> echo server log"
$K -n $NS logs pod/echo
