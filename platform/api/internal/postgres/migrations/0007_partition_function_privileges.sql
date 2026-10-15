-- The housekeeping job (runs as platform_runtime, which cannot CREATE in schema public) creates
-- access_denials partitions ahead of time. The function runs with its owner's rights and a pinned
-- search_path; EXECUTE is limited to the runtime role. Inserts through the parent table are checked
-- against the parent's grants only, so new partitions need no extra GRANT.
ALTER FUNCTION create_access_denials_partition(date) SECURITY DEFINER SET search_path = public, pg_temp;
REVOKE EXECUTE ON FUNCTION create_access_denials_partition(date) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION create_access_denials_partition(date) TO platform_runtime;
