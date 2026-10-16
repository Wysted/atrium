#!/usr/bin/env bash
# Removes the local monitoring stack: PodMonitors and PrometheusRule, the Helm release, and the `monitoring` namespace
# (with its NetworkPolicies, the dashboard ConfigMap and the grafana-admin Secret). The platform keeps running; its
# allow-ingress-metrics policies stay (they select a namespace that no longer exists).
# The Prometheus Operator CRDs stay too (Helm never removes CRDs); DROP_CRDS=1 deletes them as well.
# The generated Grafana password in .tools/secrets stays, so the next up.sh reuses it.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
cd "$ROOT"
kc() { ./tk kubectl "$@"; }

kc delete -k platform-gitops/monitoring/operator --ignore-not-found || true
./tk helm uninstall monitoring -n monitoring --wait 2>&1 || true
kc delete namespace monitoring --ignore-not-found
if [[ "${DROP_CRDS:-0}" == 1 ]]; then
  crds="$(kc get crd -o name | grep '\.monitoring\.coreos\.com$' || true)"
  # shellcheck disable=SC2086
  [[ -z "$crds" ]] || kc delete $crds
fi
echo "monitoring removed"
