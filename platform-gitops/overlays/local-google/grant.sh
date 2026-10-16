#!/usr/bin/env bash
# Grants (or with --revoke removes) a direct role of an initiative to an EXISTING user, as platform_owner.
#
#   grant.sh <email> <initiative-key> <role-key>
#   grant.sh --revoke <email> <initiative-key> <role-key>     (same as revoke.sh)
#
# The user is created by the first Google login (JIT); until then: "the user must sign in once first".
# Idempotent. Writes one audit_events row (actor_id NULL, after.actor = "cli") only when something changed.
# Platform-api picks the change up through NOTIFY (decision cache invalidated; no restart).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
cd "$ROOT"

mode=grant
if [[ "${1:-}" == --revoke ]]; then mode=revoke; shift; fi
[[ $# -eq 3 ]] || { echo "usage: $0 [--revoke] <email> <initiative-key> <role-key>" >&2; exit 2; }
email="${1,,}" initiative="$2" role="$3"

# Whitelist the inputs: they are interpolated into psql variables below (quoted with :'var'), never into the shell line.
[[ "$email" =~ ^[a-z0-9._%+-]+@[a-z0-9.-]+$ ]] || { echo "invalid email" >&2; exit 2; }
[[ "$initiative" =~ ^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$ ]] || { echo "invalid initiative key" >&2; exit 2; }
[[ "$role" =~ ^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$ ]] || { echo "invalid role key" >&2; exit 2; }

read -r -d '' SQL <<'SQLEOF' || true
\set ON_ERROR_STOP on
SELECT coalesce((SELECT id::text FROM users WHERE lower(email) = :'email' AND google_sub NOT LIKE 'fake:%'), '') AS uid \gset
SELECT :'uid' = '' AS no_user \gset
\if :no_user
  \echo 'ERROR: no user with that email: the user must sign in once first'
  \q
\endif
SELECT coalesce((SELECT r.initiative_id::text || ',' || r.id::text FROM initiative_roles r JOIN initiatives i ON i.id = r.initiative_id
                  WHERE i.key = :'initiative' AND r.key = :'role'), '') AS ir \gset
SELECT :'ir' = '' AS no_role, split_part(:'ir', ',', 1) AS iid, split_part(:'ir', ',', 2) AS rid \gset
\if :no_role
  \echo 'ERROR: unknown initiative or role'
  \q
\endif
BEGIN;
WITH change AS (
  :change_sql
), audit AS (
  INSERT INTO audit_events (actor_id, action, target_type, target_id, after)
  SELECT NULL, :'action', 'initiative_access', :'uid',
         jsonb_build_object('actor', 'cli', 'email', :'email', 'initiative', :'initiative', 'role', :'role')
  FROM change
  RETURNING 1
)
SELECT CASE WHEN count(*) > 0 THEN 'changed' ELSE 'unchanged' END FROM audit;
COMMIT;
SQLEOF

if [[ $mode == grant ]]; then
  action=access.grant
  change="INSERT INTO initiative_access (initiative_id, role_id, user_id) VALUES (:'iid'::uuid, :'rid'::uuid, :'uid'::uuid) ON CONFLICT DO NOTHING RETURNING 1"
else
  action=access.revoke
  change="DELETE FROM initiative_access WHERE initiative_id = :'iid'::uuid AND role_id = :'rid'::uuid AND user_id = :'uid'::uuid RETURNING 1"
fi
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
printf '%s\n' "${SQL//:change_sql/$change}" > "$tmp"
# ./tk allocates a TTY and runs inside the toolbox: the script is redirected there, values go through -v (never via shell).
mkdir -p .tools/tmp && cp "$tmp" .tools/tmp/grant.sql
out="$(./tk sh -c "kubectl -n platform-db exec -i postgres-0 -- psql -q -t -A -U platform_owner -d platform -v email='$email' -v initiative='$initiative' -v role='$role' -v action='$action' < .tools/tmp/grant.sql" | tr -d '\r')"
rm -f .tools/tmp/grant.sql
if [[ "$out" == ERROR:* ]]; then echo "$out" >&2; exit 1; fi
echo "$out"
echo "$mode done: $email -> $initiative/$role"
