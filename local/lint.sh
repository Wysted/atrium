#!/usr/bin/env bash
# Lint and vulnerability scan every Go module in the repo, using the pinned Go container (./gk); nothing is installed on the host.
#   local/lint.sh            # golangci-lint + govulncheck on every module
#   local/lint.sh lint       # only golangci-lint (config: .golangci.yml)
#   local/lint.sh vuln       # only govulncheck
# Covers every tracked go.mod except spikes/ (disposable code, never promoted to platform/).
# The tools run with `go run <pkg>@<version>` so they are built by the same Go as the code (a prebuilt golangci-lint
# image older than the module's Go version refuses to lint it). Builds are cached in .tools/go/.
set -euo pipefail

readonly ROOT="$(cd "$(dirname "$0")/.." && pwd)"
readonly GOLANGCI_LINT=github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
readonly GOVULNCHECK=golang.org/x/vuln/cmd/govulncheck@v1.9.0

mode="${1:-all}"
case "$mode" in
  all | lint | vuln) ;;
  *) echo "usage: $0 [all|lint|vuln]" >&2; exit 2 ;;
esac

cd "$ROOT"
failed=0
modules=0
while IFS= read -r gomod; do
  module="$(dirname "$gomod")"
  modules=$((modules + 1))
  echo "== $module"
  if [ "$mode" != vuln ]; then
    (cd "$module" && "$ROOT/gk" go run "$GOLANGCI_LINT" run ./... </dev/null) || failed=1
  fi
  if [ "$mode" != lint ]; then
    (cd "$module" && "$ROOT/gk" go run "$GOVULNCHECK" ./... </dev/null) || failed=1
  fi
done < <(git ls-files '*go.mod' ':!spikes/')

if [ "$modules" -eq 0 ]; then
  echo "lint.sh: no tracked go.mod found (is this a git checkout?)" >&2
  exit 1
fi
exit "$failed"
