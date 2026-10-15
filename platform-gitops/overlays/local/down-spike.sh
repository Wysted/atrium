#!/usr/bin/env bash
# Removes the forwardauth-cookie spike namespaces from the cluster. The manifests stay on disk
# (spikes/forwardauth-cookie/deploy). Run it before upgrading Traefik with the platform entrypoint values:
# the spike also routes auth.apps.wysted.dev and its entrypoint middlewares reference spike-authz.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"
./tk kubectl delete namespace spike-authz spike-app1 spike-app2 --ignore-not-found --wait=true --timeout=120s
