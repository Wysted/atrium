-- Every signing key records the fingerprint of the KEK that sealed it (keys.KEK.ID: HMAC-SHA256 of a
-- fixed label keyed with the KEK, 16 bytes, base64url without padding). A replica started with another
-- KEK can then tell "wrong KEK" from "corrupt key" and refuses to rotate or revoke in the first case.
--
-- Pre-existing rows cannot be backfilled: the fingerprint can only be computed with the KEK, which never
-- reaches the database. There is no production data yet, so development rows are deleted and the
-- rotator bootstraps fresh keys on its next tick. Do not copy this pattern once keys exist in production.
ALTER TABLE signing_keys ADD COLUMN kek_id text;
DELETE FROM signing_keys;
ALTER TABLE signing_keys
    ALTER COLUMN kek_id SET NOT NULL,
    ADD CONSTRAINT signing_keys_kek_id_format CHECK (kek_id ~ '^[A-Za-z0-9_-]{22}$');
-- platform_runtime keeps its column-scoped UPDATE (0005): kek_id is written on INSERT only.
