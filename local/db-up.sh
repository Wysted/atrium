#!/usr/bin/env bash
# Start the local PostgreSQL used by platform-api (runs on the host, needs Docker):
#   local/db-up.sh
# Generates passwords and the local KEK once in .tools/secrets/, plus a pgpass file for the go service (./gk).
#
# Roles, mirroring production:
#   platform      superuser that owns the schema; used by `platform-api migrate` and by tests.
#   platform_api  login role for `platform-api serve`: not an owner, member only of platform_runtime.
# Run the server as the runtime login role:
#   ./gk env PGUSER=platform_api go run ./cmd/platform-api serve
set -euo pipefail

readonly ROOT="$(cd "$(dirname "$0")/.." && pwd)"
readonly SECRETS="$ROOT/.tools/secrets"
readonly OWNER_PASSWORD_FILE="$SECRETS/postgres-password"
readonly API_PASSWORD_FILE="$SECRETS/platform-api-db-password"
readonly PGPASS_FILE="$SECRETS/pgpass"
readonly PASSWORD_BYTES=32
readonly KEK_FILE="$SECRETS/platform-kek"
readonly KEK_BYTES=32
# Group-readable like a Kubernetes Secret mounted with fsGroup; platform-api rejects anything looser than 0640.
readonly KEK_MODE=0440
readonly OWNER_ROLE=platform
readonly API_ROLE=platform_api
readonly RUNTIME_ROLE=platform_runtime

cd "$ROOT"
umask 077
mkdir -p "$SECRETS" .tools/postgres

for file in "$OWNER_PASSWORD_FILE" "$API_PASSWORD_FILE"; do
  if [[ ! -s "$file" ]]; then
    openssl rand -hex "$PASSWORD_BYTES" | tr -d '\n' > "$file"
  fi
done

# Local key-encryption key for platform-api (PLATFORM_KEK_FILE). Generated once and kept stable: signing keys
# sealed with it become unusable if it changes (the server then reports KEKMismatch and stops signing).
if [[ ! -s "$KEK_FILE" ]]; then
  openssl rand "$KEK_BYTES" > "$KEK_FILE"
fi
chmod "$KEK_MODE" "$KEK_FILE"
{
  printf 'postgres:5432:*:%s:%s\n' "$OWNER_ROLE" "$(cat "$OWNER_PASSWORD_FILE")"
  printf 'postgres:5432:*:%s:%s\n' "$API_ROLE" "$(cat "$API_PASSWORD_FILE")"
} > "$PGPASS_FILE"

export LOCAL_UID="$(id -u)"
export LOCAL_GID="$(id -g)"
docker compose up -d --wait postgres

# The runtime group role is normally created by the migrations; create it here too so the login role can
# join it before the first migrate. The password travels through psql's stdin, never the command line.
{
  # printf is a shell builtin: the password never shows up in any process argument list.
  printf "\\set api_password '%s'\n" "$(<"$API_PASSWORD_FILE")"
  cat <<'SQL'
SELECT format('CREATE ROLE %I NOLOGIN', :'runtime_role')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'runtime_role') \gexec
SELECT format('CREATE ROLE %I LOGIN', :'api_role')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'api_role') \gexec
SELECT format('ALTER ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L',
              :'api_role', :'api_password') \gexec
SELECT format('GRANT %I TO %I', :'runtime_role', :'api_role') \gexec
SQL
} | docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -q -U "$OWNER_ROLE" -d platform \
  -v api_role="$API_ROLE" -v runtime_role="$RUNTIME_ROLE"

docker compose ps postgres
