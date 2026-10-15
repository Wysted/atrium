-- Block 4: authorization data model (RBAC with domains: roles live inside an initiative).
-- Requires PostgreSQL >= 18 (uuidv7()).

CREATE TABLE users (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    google_sub text        NOT NULL UNIQUE,
    email      text        NOT NULL,
    name       text        NOT NULL DEFAULT '',
    status     text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE groups (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    key        text        NOT NULL UNIQUE CHECK (key ~ '^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$'),
    name       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE group_members (
    group_id uuid        NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    user_id  uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user_id_idx ON group_members (user_id);

CREATE TABLE initiatives (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    key        text        NOT NULL UNIQUE CHECK (key ~ '^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$'),
    name       text        NOT NULL DEFAULT '',
    status     text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE initiative_roles (
    id             uuid    PRIMARY KEY DEFAULT uuidv7(),
    initiative_id  uuid    NOT NULL REFERENCES initiatives (id),
    key            text    NOT NULL CHECK (key ~ '^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$'),
    manages_access boolean NOT NULL DEFAULT false,
    UNIQUE (initiative_id, key),
    UNIQUE (initiative_id, id) -- target of the composite FK from initiative_access
);

-- apps.id is the JWT "aud": opaque, immutable, app_<ULID> (Crockford base32, uppercase).
CREATE TABLE apps (
    id            text        PRIMARY KEY CHECK (id ~ '^app_[0-9A-HJKMNP-TV-Z]{26}$'),
    initiative_id uuid        NOT NULL REFERENCES initiatives (id),
    name          text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX apps_initiative_id_idx ON apps (initiative_id);

CREATE TABLE deployments (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    host       text        NOT NULL UNIQUE CHECK (host = lower(host) AND host ~ '^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$'),
    app_id     text        NOT NULL REFERENCES apps (id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deployments_app_id_idx ON deployments (app_id);

CREATE TABLE initiative_access (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    initiative_id uuid        NOT NULL,
    role_id       uuid        NOT NULL,
    user_id       uuid        REFERENCES users (id) ON DELETE CASCADE,
    group_id      uuid        REFERENCES groups (id) ON DELETE CASCADE,
    expires_at    timestamptz,
    granted_by    uuid        REFERENCES users (id) ON DELETE SET NULL,
    granted_at    timestamptz NOT NULL DEFAULT now(),
    -- A role of initiative A can never be granted on initiative B.
    FOREIGN KEY (initiative_id, role_id) REFERENCES initiative_roles (initiative_id, id),
    CONSTRAINT initiative_access_subject_xor CHECK (num_nonnulls(user_id, group_id) = 1)
);
CREATE UNIQUE INDEX initiative_access_user_uq
    ON initiative_access (initiative_id, role_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX initiative_access_group_uq
    ON initiative_access (initiative_id, role_id, group_id) WHERE group_id IS NOT NULL;
CREATE INDEX initiative_access_user_idx  ON initiative_access (user_id)  WHERE user_id IS NOT NULL;
CREATE INDEX initiative_access_group_idx ON initiative_access (group_id) WHERE group_id IS NOT NULL;

CREATE TABLE platform_admins (
    user_id    uuid        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    granted_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    granted_at timestamptz NOT NULL DEFAULT now()
);

-- Append-only: the runtime role only gets INSERT and SELECT. No FKs so history survives deletions.
CREATE TABLE audit_events (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    occurred_at timestamptz NOT NULL DEFAULT now(),
    actor_id    uuid,
    action      text        NOT NULL,
    target_type text        NOT NULL,
    target_id   text        NOT NULL,
    before      jsonb,
    after       jsonb
);
CREATE INDEX audit_events_occurred_at_idx ON audit_events (occurred_at);

-- Deny decisions, partitioned by month (UTC). Retention (365 days) = dropping old partitions.
CREATE TABLE access_denials (
    id            uuid        NOT NULL DEFAULT uuidv7(),
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    user_id       uuid,
    host          text        NOT NULL,
    app_id        text,
    initiative_id uuid,
    reason        text        NOT NULL,
    sid           uuid,
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE FUNCTION create_access_denials_partition(p_month date) RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    start_ts timestamptz := (date_trunc('month', p_month)::timestamp AT TIME ZONE 'UTC');
    end_ts   timestamptz := ((date_trunc('month', p_month) + interval '1 month')::timestamp AT TIME ZONE 'UTC');
    part     text := format('access_denials_%s', to_char(date_trunc('month', p_month), 'YYYY_MM'));
BEGIN
    EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF access_denials FOR VALUES FROM (%L) TO (%L)',
                   part, start_ts, end_ts);
    RETURN part;
END
$$;

SELECT create_access_denials_partition(current_date);
SELECT create_access_denials_partition((current_date + interval '1 month')::date);
