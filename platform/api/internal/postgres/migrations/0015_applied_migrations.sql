-- Readiness gate for the schema (docs/operate.md, "Upgrade"). platform_runtime and platform_portal cannot read
-- schema_migrations (least privilege, asserted by the role checks), yet each process must know whether the
-- migrate Job has applied every migration embedded in its binary. This narrow SECURITY DEFINER function is the
-- only way they see it: it returns the applied versions (file names, no timestamps) and nothing else.
CREATE FUNCTION applied_migrations() RETURNS SETOF text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT version FROM schema_migrations
$$;
REVOKE EXECUTE ON FUNCTION applied_migrations() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION applied_migrations() TO platform_runtime, platform_portal;
