-- Least privilege for platform_runtime. The portal (platform_portal, 0009) owns administration of the
-- authorization tables (contract block 6), so platform-api keeps only what ForwardAuth and login need. Every
-- grant below maps to a statement in internal/postgres/*_repo.go; CheckRuntimeRole refuses to start if the role
-- holds anything else. Never edit applied migrations.
--
-- Deployment ordering: the migrate Job must complete before platform-api (and the portal) roll out. New pods
-- check the grants of this migration at start-up and refuse to run (fail closed) against the old ones.

-- Start from nothing on every table the role could touch (this also drops column-level grants), then grant
-- exactly what is used. signing_keys (0005), audit_events and access_denials (0008) and the partition
-- functions (0007, 0008) are not touched here, nor is schema_migrations (no access since 0008).
REVOKE ALL ON
    users, groups, group_members, initiatives, initiative_roles, apps, deployments, initiative_access,
    platform_admins, oauth_flows, sessions, app_sessions, login_codes
    FROM platform_runtime;

-- Read-only facts for the ForwardAuth decision.
GRANT SELECT ON group_members, initiatives, initiative_roles, apps, initiative_access TO platform_runtime;

-- Just-in-time login: INSERT ... ON CONFLICT DO UPDATE of the informational fields only. status (disabling a
-- user) and google_sub (identity) are never writable from platform-api.
GRANT SELECT, INSERT ON users TO platform_runtime;
GRANT UPDATE (email, name, updated_at) ON users TO platform_runtime;

-- Sessions: created at login, touched by validation (last_seen_at only), deleted at logout and by housekeeping.
GRANT SELECT, INSERT, DELETE ON sessions TO platform_runtime;
GRANT UPDATE (last_seen_at) ON sessions TO platform_runtime;
GRANT SELECT, INSERT, DELETE ON app_sessions TO platform_runtime;

-- One-time login state: consumed once, then purged.
GRANT SELECT, INSERT, DELETE ON login_codes, oauth_flows TO platform_runtime;
GRANT UPDATE (consumed_at) ON login_codes, oauth_flows TO platform_runtime;

-- Last-seen telemetry: SELECT ... FOR NO KEY UPDATE SKIP LOCKED needs UPDATE on at least one column, and
-- last_seen_at is the one it writes. host and app_id (which decide access) stay read-only.
GRANT SELECT ON deployments TO platform_runtime;
GRANT UPDATE (last_seen_at) ON deployments TO platform_runtime;
