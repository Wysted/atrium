-- Runtime role hardening (never edit applied migrations).
--
-- 1. create_access_denials_partition only accepts months in [current month - 1, current month + 12] (UTC):
--    a bug or abuse cannot create thousands of partitions. The function stays SECURITY DEFINER with a
--    pinned search_path (0007); CREATE OR REPLACE resets attributes, so they are restated.
-- 2. drop_expired_access_denials_partitions implements the 365-day retention of access_denials. DROP TABLE
--    needs ownership, which platform_runtime does not have, so it is a narrow SECURITY DEFINER function:
--    it only touches partitions named access_denials_YYYY_MM of the parent, only those whose whole month is
--    older than the retention, and refuses retentions under 30 days.
-- 3. Explicit statement of the runtime privileges the application relies on, and the ones it must never get.

CREATE OR REPLACE FUNCTION create_access_denials_partition(p_month date) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    cur_month timestamp := date_trunc('month', now() AT TIME ZONE 'UTC');
    req_month timestamp := date_trunc('month', p_month::timestamp);
    start_ts  timestamptz := req_month AT TIME ZONE 'UTC';
    end_ts    timestamptz := (req_month + interval '1 month') AT TIME ZONE 'UTC';
    part      text := format('access_denials_%s', to_char(req_month, 'YYYY_MM'));
BEGIN
    IF req_month < cur_month - interval '1 month' OR req_month > cur_month + interval '12 months' THEN
        RAISE EXCEPTION 'access_denials partition month % is outside [%, %]',
            to_char(req_month, 'YYYY-MM'),
            to_char(cur_month - interval '1 month', 'YYYY-MM'),
            to_char(cur_month + interval '12 months', 'YYYY-MM')
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF access_denials FOR VALUES FROM (%L) TO (%L)',
                   part, start_ts, end_ts);
    RETURN part;
END
$$;
REVOKE EXECUTE ON FUNCTION create_access_denials_partition(date) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION create_access_denials_partition(date) TO platform_runtime;

CREATE FUNCTION drop_expired_access_denials_partitions(p_retention_days integer) RETURNS SETOF text
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    cutoff   timestamptz;
    part     text;
    part_end timestamptz;
BEGIN
    IF p_retention_days IS NULL OR p_retention_days < 30 THEN
        RAISE EXCEPTION 'access_denials retention must be at least 30 days, got %', p_retention_days
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    cutoff := now() - make_interval(days => p_retention_days);
    -- DROP takes ACCESS EXCLUSIVE on the parent: do not queue behind (and block) denial inserts for long.
    PERFORM set_config('lock_timeout', '2s', true);
    FOR part IN
        SELECT c.relname::text
        FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.access_denials'::regclass
          AND c.relname ~ '^access_denials_[0-9]{4}_[0-9]{2}$'
        ORDER BY c.relname
    LOOP
        part_end := (to_date(substr(part, 16), 'YYYY_MM')::timestamp + interval '1 month') AT TIME ZONE 'UTC';
        IF part_end <= cutoff THEN
            EXECUTE format('DROP TABLE public.%I', part);
            RETURN NEXT part;
        END IF;
    END LOOP;
END
$$;
REVOKE EXECUTE ON FUNCTION drop_expired_access_denials_partitions(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION drop_expired_access_denials_partitions(integer) TO platform_runtime;

-- Re-assert: the runtime role never gets more than this on the append-only and key tables, and nothing on
-- the migration bookkeeping.
REVOKE UPDATE, DELETE, TRUNCATE ON audit_events FROM platform_runtime;
REVOKE UPDATE, DELETE, TRUNCATE ON access_denials FROM platform_runtime;
REVOKE ALL ON schema_migrations FROM platform_runtime;
