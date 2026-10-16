#!/usr/bin/env bash
# Checks the alert rules without a cluster, using the pinned Prometheus image (promtool) through Docker:
#   1. operator/prometheusrule.yaml is in sync with rules/portal-platform.rules.yaml (gen-prometheusrule.sh --check);
#   2. promtool check rules on the plain file;
#   3. promtool test rules: firing and not-firing cases of every rule (rules/tests/).
# Nothing is installed on the host; local/lint.sh runs this too.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Same version as the Prometheus the local stack runs (overlays/local/monitoring/values.yaml); pinned by tag and index digest.
readonly PROMETHEUS_IMAGE=prom/prometheus:v3.15.0@sha256:efd719c99d83b060d9daefdcf00360461adf279f45ef5391f8d111892118753e

echo "== PrometheusRule in sync with the plain rules"
"$HERE/gen-prometheusrule.sh" --check

# --entrypoint: the image's entrypoint is prometheus itself. Read-only mount, no network, non-root.
promtool() {
  docker run --rm --network none --user "$(id -u):$(id -g)" --entrypoint /bin/promtool \
    -v "$HERE/rules:/rules:ro" -w /rules "$PROMETHEUS_IMAGE" "$@"
}

echo "== promtool check rules"
promtool check rules /rules/portal-platform.rules.yaml
echo "== promtool test rules"
promtool test rules /rules/tests/portal-platform.test.yaml
