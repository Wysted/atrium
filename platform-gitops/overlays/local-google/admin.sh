#!/usr/bin/env bash
# Makes an EXISTING real user a platform admin (or with --revoke removes it), as platform_owner. The portal's admin API
# (/api/v1/groups, /api/v1/initiatives, ...) needs it; the portal has no endpoint to manage platform admins by design.
#
#   admin.sh <email>
#   admin.sh --revoke <email>
#
# The user is created by the first Google login (JIT): until then "the user must sign in once first". Fake users
# (fake:*) are never touched. Idempotent. Writes one audit_events row (actor_id NULL, after.actor = "cli") only when
# something changed. DEVELOPMENT tooling: in production admins are managed through the platform's own process.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
cd "$ROOT"

mode=grant
if [[ "${1:-}" == --revoke ]]; then mode=revoke; shift; fi
[[ $# -eq 1 ]] || { echo "usage: $0 [--revoke] <email>" >&2; exit 2; }
email="${1,,}"
[[ "$email" =~ ^[a-z0-9._%+-]+@[a-z0-9.-]+$ ]] || { echo "invalid email" >&2; exit 2; }

read -r -d '' SQL <<'SQLEOF' || true
\set ON_ERROR_STOP on
SELECT coalesce((SELECT id::text FROM users WHERE lower(email) = :'email' AND kind = 'human' AND google_sub NOT LIKE 'fake:%'), '') AS uid \gset
SELECT :'uid' = '' AS no_user \gset
\if :no_user
  \echo 'ERROR: no user with that email: the user must sign in once first'
  \q
\endif
BEGIN;
WITH change AS (
  :change_sql
), audit AS (
  INSERT INTO audit_events (actor_id, action, target_type, target_id, after)
  SELECT NULL, :'action', 'platform_admin', :'uid', jsonb_build_object('actor', 'cli', 'email', :'email')
  FROM change
  RETURNING 1
)
SELECT CASE WHEN count(*) > 0 THEN 'changed' ELSE 'unchanged' END FROM audit;
COMMIT;
SQLEOF

if [[ $mode == grant ]]; then
  action=platform_admin.grant
  change="INSERT INTO platform_admins (user_id) VALUES (:'uid'::uuid) ON CONFLICT DO NOTHING RETURNING 1"
else
  action=platform_admin.revoke
  change="DELETE FROM platform_admins WHERE user_id = :'uid'::uuid RETURNING 1"
fi
tmp="$(mktemp)"; trap 'rm -f "$tmp" .tools/tmp/admin.sql' EXIT
printf '%s\n' "${SQL//:change_sql/$change}" > "$tmp"
# ./tk allocates a TTY and runs inside the toolbox: the script is redirected there, values go through -v (never via shell).
mkdir -p .tools/tmp && cp "$tmp" .tools/tmp/admin.sql
out="$(./tk sh -c "kubectl -n platform-db exec -i postgres-0 -- psql -q -t -A -U platform_owner -d platform -v email='$email' -v action='$action' < .tools/tmp/admin.sql" | tr -d '\r')"
if [[ "$out" == ERROR:* ]]; then echo "$out" >&2; exit 1; fi
echo "$out"
echo "$mode done: $email is $([[ $mode == grant ]] && echo now || echo no longer) a platform admin"
