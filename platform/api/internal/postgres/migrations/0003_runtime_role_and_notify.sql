-- Runtime role: NOLOGIN for now, minimal grants. Roles are cluster-wide, so creation tolerates races.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'platform_runtime') THEN
        CREATE ROLE platform_runtime NOLOGIN;
    END IF;
EXCEPTION WHEN duplicate_object OR unique_violation THEN
    NULL;
END
$$;

GRANT USAGE ON SCHEMA public TO platform_runtime;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    users, groups, group_members, initiatives, initiative_roles, apps, deployments,
    initiative_access, platform_admins, oauth_flows, sessions, app_sessions, login_codes
    TO platform_runtime;
GRANT SELECT, INSERT, UPDATE ON signing_keys TO platform_runtime;
GRANT SELECT, INSERT ON audit_events TO platform_runtime;
GRANT SELECT, INSERT ON access_denials TO platform_runtime;

-- Any change that can alter an access decision emits NOTIFY access_changed (payload: table name).
-- The decision cache (TTL 30 s) is invalidated by LISTEN.
CREATE FUNCTION notify_access_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('access_changed', TG_TABLE_NAME);
    RETURN NULL;
END
$$;

CREATE TRIGGER users_access_changed
    AFTER UPDATE OF status OR DELETE ON users
    FOR EACH STATEMENT EXECUTE FUNCTION notify_access_changed();
CREATE TRIGGER initiatives_access_changed
    AFTER INSERT OR UPDATE OF status OR DELETE ON initiatives
    FOR EACH STATEMENT EXECUTE FUNCTION notify_access_changed();
CREATE TRIGGER group_members_access_changed
    AFTER INSERT OR UPDATE OR DELETE ON group_members
    FOR EACH STATEMENT EXECUTE FUNCTION notify_access_changed();
CREATE TRIGGER apps_access_changed
    AFTER INSERT OR UPDATE OR DELETE ON apps
    FOR EACH STATEMENT EXECUTE FUNCTION notify_access_changed();
CREATE TRIGGER deployments_access_changed
    AFTER INSERT OR UPDATE OR DELETE ON deployments
    FOR EACH STATEMENT EXECUTE FUNCTION notify_access_changed();
CREATE TRIGGER initiative_roles_access_changed
    AFTER INSERT OR UPDATE OR DELETE ON initiative_roles
    FOR EACH STATEMENT EXECUTE FUNCTION notify_access_changed();
CREATE TRIGGER initiative_access_access_changed
    AFTER INSERT OR UPDATE OR DELETE ON initiative_access
    FOR EACH STATEMENT EXECUTE FUNCTION notify_access_changed();
