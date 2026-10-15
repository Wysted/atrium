#!/usr/bin/env bash
# Loads the development seed as platform_owner (local socket of the postgres pod: kubectl exec, no password).
# Idempotent; also restores grants revoked by smoke.sh.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
cd "$ROOT"
# ./tk allocates a TTY, so the SQL file is redirected inside the toolbox shell instead of piped from here.
./tk sh -c "kubectl -n platform-db exec -i postgres-0 -- psql -v ON_ERROR_STOP=1 -q -U platform_owner -d platform < platform-gitops/overlays/local/seed.sql"
echo "seed applied"
