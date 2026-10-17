#!/usr/bin/env bash
# Back to the fake login (overlay local): reapplies it, removes the Google egress policy, client secret and client id.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
cd "$ROOT"
kc() { ./tk kubectl "$@"; }

kc apply -k platform-gitops/overlays/local/platform
# apply does not prune: the Google-only resources and the volume stay unless removed explicitly.
kc -n platform delete networkpolicy allow-egress-google --ignore-not-found
kc -n platform delete secret platform-google-client-secret --ignore-not-found
kc -n platform delete configmap platform-google-client-id --ignore-not-found
kc -n platform rollout status deploy/platform-api --timeout=240s
