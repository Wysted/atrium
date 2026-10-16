-- Database roles that cannot be created by the migrations (platform_owner has no CREATEROLE). Run by roles.sh as the
-- bootstrap superuser over the postgres pod's local socket. Idempotent; works on a fresh and on an existing volume
-- (initdb.sh only runs on the first initialization of the data directory).
--
--   platform_portal  NOLOGIN group role of the portal process. Migration 0009 creates it only IF NOT EXISTS, so
--                    it must exist beforehand (the owner cannot create roles).
--   portal_app       LOGIN used by `portal serve`: not a superuser, owns nothing, member only of platform_portal.
--
-- :portal_password is set by roles.sh (\set) from a file; it never travels in an argument list.
SELECT 'CREATE ROLE platform_portal NOLOGIN'
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'platform_portal') \gexec
SELECT 'CREATE ROLE portal_app LOGIN'
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'portal_app') \gexec
-- \gexec runs the generated ALTER ROLE ... PASSWORD '<secret>' as a statement: if it fails, the server would log the whole
-- statement (the password included) at the default log_min_error_statement=error. Only PANIC is never reached here.
SET log_min_error_statement = 'panic';
SELECT format('ALTER ROLE portal_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', :'portal_password') \gexec
GRANT platform_portal TO portal_app;
-- initdb.sh revoked CONNECT from PUBLIC: every login role needs its own.
GRANT CONNECT ON DATABASE platform TO portal_app;
