-- Portal app (contract block 6): audit scoping, app deactivation, integration health, and the role of the
-- portal process. Never edit applied migrations.

-- Managers filter the audit log by column instead of parsing jsonb. No FK, like the rest of the table.
ALTER TABLE audit_events ADD COLUMN initiative_id uuid;
CREATE INDEX audit_events_initiative_idx ON audit_events (initiative_id, occurred_at);

-- Deactivate instead of delete. The apps_access_changed trigger (0003) fires on every UPDATE of apps, status
-- included. Renames also flush the decision caches; they are rare, so the trigger is left as is.
ALTER TABLE apps ADD COLUMN status text NOT NULL DEFAULT 'active';
ALTER TABLE apps ADD CONSTRAINT apps_status_check CHECK (status IN ('active', 'disabled'));

-- Integration health. last_probe_status is a closed set:
--   ok           the host answered a redirect (302) to the auth host: ForwardAuth protects it
--   unprotected  the host answered without ForwardAuth (any non-redirect response)
--   unexpected   the host redirected somewhere other than the auth host
--   unreachable  network or TLS error
-- NULL means never probed.
ALTER TABLE deployments
    ADD COLUMN last_seen_at      timestamptz,
    ADD COLUMN last_probe_at     timestamptz,
    ADD COLUMN last_probe_status text;
ALTER TABLE deployments ADD CONSTRAINT deployments_last_probe_status_check
    CHECK (last_probe_status IN ('ok', 'unprotected', 'unexpected', 'unreachable'));

-- last_seen_at and the probe columns are written every minute or so and do not change any decision: only
-- INSERT, DELETE and a change of host or app_id may flush the ForwardAuth caches (same function as 0003).
DROP TRIGGER deployments_access_changed ON deployments;
CREATE TRIGGER deployments_access_changed
    AFTER INSERT OR DELETE OR UPDATE OF host, app_id ON deployments
    FOR EACH STATEMENT EXECUTE FUNCTION notify_access_changed();

-- Role of the portal process (cmd/portal). NOLOGIN like platform_runtime; roles are cluster-wide, so creation
-- tolerates races. No sequences or functions are involved (ids come from uuidv7()).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'platform_portal') THEN
        CREATE ROLE platform_portal NOLOGIN;
    END IF;
EXCEPTION WHEN duplicate_object OR unique_violation THEN
    NULL;
END
$$;

GRANT USAGE ON SCHEMA public TO platform_portal;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    groups, group_members, initiatives, initiative_roles, apps, deployments, initiative_access
    TO platform_portal;
-- Users are created just-in-time by platform-api; the portal only reads them and disables/enables them
-- (status, updated_at).
-- google_sub, email and name are never writable from the portal.
GRANT SELECT ON users TO platform_portal;
GRANT UPDATE (status, updated_at) ON users TO platform_portal;
GRANT SELECT ON platform_admins TO platform_portal;
GRANT SELECT, INSERT ON audit_events TO platform_portal;
GRANT SELECT ON access_denials TO platform_portal;
-- Session revocation: the portal can see and delete sessions but never read the token hashes.
GRANT SELECT (id, user_id, created_at, last_seen_at, expires_at, ip, user_agent) ON sessions TO platform_portal;
GRANT SELECT (id, session_id, app_id, host, created_at, expires_at) ON app_sessions TO platform_portal;
GRANT DELETE ON sessions, app_sessions TO platform_portal;

-- Explicitly nothing on keys, login state or migration bookkeeping (and audit_events stays append-only).
REVOKE ALL ON signing_keys, oauth_flows, login_codes, schema_migrations FROM platform_portal;
REVOKE UPDATE, DELETE, TRUNCATE ON audit_events FROM platform_portal;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON access_denials FROM platform_portal;
