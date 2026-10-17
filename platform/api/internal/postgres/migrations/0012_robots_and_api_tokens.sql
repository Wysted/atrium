-- Non-browser clients, phase 1 (contract block 6, "Non-browser clients"): robots as users of kind 'robot',
-- API tokens and their privileges. Never edit applied migrations.
--
-- Deployment ordering: the migrate Job must complete before platform-api (and the portal) roll out. New pods
-- check the grants of this migration at start-up and refuse to run (fail closed) against the old ones, and the
-- other way round during a rollback.

-- ALTER TABLE users takes ACCESS EXCLUSIVE, and ForwardAuth reads users on every request: do not queue behind a
-- long transaction (and block every reader behind this statement). The migration fails and is retried instead.
-- Transaction-local, like the retention function of 0008.
SELECT set_config('lock_timeout', '5s', true);

-- ---- users: kind, nullable google_sub, robot fields ----
-- Existing rows become humans (the default) and keep their google_sub, so every new CHECK holds for them.
ALTER TABLE users
    ADD COLUMN kind          text NOT NULL DEFAULT 'human',
    -- The accountable human of a robot: informational, not authority. The API checks it is an active human.
    ADD COLUMN owner_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    ALTER COLUMN google_sub DROP NOT NULL,
    ADD CONSTRAINT users_kind_check CHECK (kind IN ('human', 'robot')),
    -- A robot has no google_sub (a Google login can never resolve to it); a human always has one.
    ADD CONSTRAINT users_google_sub_kind CHECK ((kind = 'human') = (google_sub IS NOT NULL)),
    ADD CONSTRAINT users_robot_email CHECK (kind = 'human' OR email = ''),
    ADD CONSTRAINT users_robot_name_length CHECK (kind = 'human' OR char_length(name) BETWEEN 1 AND 64),
    -- Target of the composite foreign keys that keep robots out of platform_admins and group_members.
    ADD CONSTRAINT users_id_kind_key UNIQUE (id, kind);
CREATE UNIQUE INDEX users_robot_name_uq ON users (lower(name)) WHERE kind = 'robot';

-- ---- platform_admins, group_members: only humans ----
-- The composite foreign key replaces the single-column one under the same name, so the portal's mapping of
-- violated foreign keys to the request field user_id (referenceFields) stays correct.
ALTER TABLE platform_admins DROP CONSTRAINT platform_admins_user_id_fkey;
ALTER TABLE platform_admins
    ADD COLUMN kind text NOT NULL DEFAULT 'human' CONSTRAINT platform_admins_kind_check CHECK (kind = 'human'),
    ADD CONSTRAINT platform_admins_user_id_fkey FOREIGN KEY (user_id, kind) REFERENCES users (id, kind) ON DELETE CASCADE;

ALTER TABLE group_members DROP CONSTRAINT group_members_user_id_fkey;
ALTER TABLE group_members
    ADD COLUMN kind text NOT NULL DEFAULT 'human' CONSTRAINT group_members_kind_check CHECK (kind = 'human'),
    ADD CONSTRAINT group_members_user_id_fkey FOREIGN KEY (user_id, kind) REFERENCES users (id, kind) ON DELETE CASCADE;

-- ---- api_tokens ----
-- The secret is never stored: token_hash is the SHA-256 of the whole secret (256 bits of entropy, so no slow hash).
CREATE TABLE api_tokens (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(), -- the "tid" claim and the audit target
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE, -- owner, human or robot
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    token_hash   bytea       NOT NULL CHECK (octet_length(token_hash) = 32),
    scope        text        NOT NULL CHECK (scope IN ('read', 'admin')),
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz,
    CONSTRAINT api_tokens_expires_after_created CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX api_tokens_token_hash_key ON api_tokens (token_hash); -- lookup on every token request
CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id); -- own tokens, live count, revocation on disable

-- ---- audit: which token acted ----
-- No FK, like the rest of the table. No index: nothing filters by it yet.
ALTER TABLE audit_events ADD COLUMN token_id uuid;

-- ---- purge of dead tokens ----
-- platform_runtime has no DELETE on api_tokens; like drop_expired_access_denials_partitions (0008) this is a
-- narrow SECURITY DEFINER function. A token is dead from the earlier of expires_at and revoked_at; only rows
-- dead for longer than the grace period go, and a grace under 7 days is refused (a bug cannot purge fresh rows).
CREATE FUNCTION purge_dead_api_tokens(p_grace interval) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    n bigint;
BEGIN
    IF p_grace IS NULL OR p_grace < interval '7 days' THEN
        RAISE EXCEPTION 'api_tokens grace must be at least 7 days, got %', p_grace
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    DELETE FROM api_tokens
     WHERE LEAST(expires_at, COALESCE(revoked_at, expires_at)) < now() - p_grace;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;
REVOKE EXECUTE ON FUNCTION purge_dead_api_tokens(interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_dead_api_tokens(interval) TO platform_runtime;

-- ---- privileges: platform_runtime ----
-- ForwardAuth reads tokens (including the hash) and records last use; it cannot insert, revoke or delete them.
GRANT SELECT ON api_tokens TO platform_runtime;
GRANT UPDATE (last_used_at) ON api_tokens TO platform_runtime;
-- Login upsert columns only: a compromised runtime cannot create a robot (kind, owner) or set a status.
REVOKE INSERT ON users FROM platform_runtime;
GRANT INSERT (google_sub, email, name) ON users TO platform_runtime;

-- ---- privileges: platform_portal ----
-- Robots only: without google_sub in the column list the CHECK rejects a human.
GRANT INSERT (kind, name, email, owner_user_id, status) ON users TO platform_portal;
GRANT UPDATE (name, owner_user_id) ON users TO platform_portal;
-- Tokens: created and revoked here, never read back with the hash and never deleted (purge is the function above).
GRANT INSERT ON api_tokens TO platform_portal;
GRANT SELECT (id, user_id, name, scope, created_by, created_at, expires_at, last_used_at, revoked_at)
    ON api_tokens TO platform_portal;
GRANT UPDATE (revoked_at) ON api_tokens TO platform_portal;
