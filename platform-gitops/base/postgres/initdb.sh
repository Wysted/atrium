#!/usr/bin/env bash
# Runs once, on first initialization of the data directory (docker-entrypoint-initdb.d), as the bootstrap
# superuser over the local socket. Passwords come from files (Secret mount) and travel through psql's stdin:
# never in an argument list or in a manifest.
#
# Roles (mirrors production):
#   postgres_admin    bootstrap superuser; locked out of TCP at the end of this script.
#   platform_owner    LOGIN, no superuser, no CREATEROLE; owns the database and the schema (migrate).
#   platform_runtime  NOLOGIN group role. Created here because platform_owner has no CREATEROLE: migration 0003
#                     creates it only IF NOT EXISTS, and platform_api needs to be a member beforehand.
#   platform_api      LOGIN used by `platform-api serve`; member only of platform_runtime.
set -euo pipefail

secrets=/run/secrets/db
owner_pw="$(<"$secrets/owner-password")"
api_pw="$(<"$secrets/api-password")"

{
  printf "\\set owner_password '%s'\n" "$owner_pw"
  printf "\\set api_password '%s'\n" "$api_pw"
  cat <<'SQL'
CREATE ROLE platform_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD :'owner_password';
CREATE ROLE platform_runtime NOLOGIN;
CREATE ROLE platform_api LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD :'api_password';
GRANT platform_runtime TO platform_api;
-- The database must belong to platform_owner: since PostgreSQL 15 schema public is owned by pg_database_owner,
-- and migration 0003 runs GRANT USAGE ON SCHEMA public as the owner.
CREATE DATABASE platform OWNER platform_owner;
REVOKE ALL ON DATABASE platform FROM PUBLIC;
GRANT CONNECT ON DATABASE platform TO platform_owner, platform_api;
SQL
} | psql -v ON_ERROR_STOP=1 -q -U "$POSTGRES_USER" -d postgres

# The superuser stays reachable through the local socket (kubectl exec) but never over TCP.
hba="$PGDATA/pg_hba.conf"
{
  printf 'host all %s 0.0.0.0/0 reject\n' "$POSTGRES_USER"
  printf 'host all %s ::/0 reject\n' "$POSTGRES_USER"
  cat "$hba"
} > "$hba.new"
mv "$hba.new" "$hba"
