-- deployments.host follows the same rule as hostname.Valid (contract block 6, Hosts): ForwardAuth refuses any
-- host that fails it before the lookup, so a row the old CHECK (0001) allowed but Valid refuses (a..b, a numeric
-- last label) could never resolve. Never edit applied migrations.
--
-- The rule: lowercase DNS name of at most 253 bytes, labels of 1 to 63 characters from [a-z0-9-] that neither
-- start nor end with a hyphen, no trailing dot, no wildcard, no port, last label not all digits.
--
-- Rows that violate it make this migration fail (VALIDATE CONSTRAINT) and roll back; the runner applies each file
-- in one transaction, so NOT VALID does not shorten the lock, it only separates adding from checking. Find them
-- with the query below, delete or fix them (and their app routing), then run the migration again:
--   SELECT id, host FROM deployments
--   WHERE NOT (octet_length(host) <= 253
--     AND host ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
--     AND host !~ '(^|\.)[0-9]+$');
ALTER TABLE deployments DROP CONSTRAINT deployments_host_check;
ALTER TABLE deployments ADD CONSTRAINT deployments_host_valid CHECK (
    octet_length(host) <= 253
    AND host ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
    AND host !~ '(^|\.)[0-9]+$'
) NOT VALID;
ALTER TABLE deployments VALIDATE CONSTRAINT deployments_host_valid;
