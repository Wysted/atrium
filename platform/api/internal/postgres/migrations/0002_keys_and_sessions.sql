-- Block 3: signing keys.
CREATE TABLE signing_keys (
    kid         text        PRIMARY KEY CHECK (kid ~ '^[A-Za-z0-9_-]{43}$'), -- RFC 7638 thumbprint, base64url SHA-256
    public_jwk  jsonb       NOT NULL,
    private_enc bytea,                -- envelope-encrypted; deleted when the key is removed
    state       text        NOT NULL CHECK (state IN ('next', 'active', 'retired', 'removed')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz,
    retired_at  timestamptz,
    removed_at  timestamptz,
    CONSTRAINT signing_keys_private_material CHECK ((state = 'removed') = (private_enc IS NULL))
);
CREATE UNIQUE INDEX signing_keys_single_active_uq ON signing_keys (state) WHERE state = 'active';

-- Block 5: login flows and sessions. Token values are 256-bit random; only SHA-256 is stored.
CREATE TABLE oauth_flows (
    token_hash    bytea       PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    state         text        NOT NULL,
    nonce         text        NOT NULL,
    code_verifier text        NOT NULL,
    rd            text        NOT NULL,
    bind_hash     bytea       NOT NULL CHECK (octet_length(bind_hash) = 32),
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    consumed_at   timestamptz
);
CREATE INDEX oauth_flows_expires_at_idx ON oauth_flows (expires_at);

CREATE TABLE sessions (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(), -- public sid (JWT "sid"), cannot authenticate
    token_hash   bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL, -- absolute lifetime
    ip           inet,
    user_agent   text
);
CREATE INDEX sessions_user_id_idx    ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE app_sessions (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    token_hash bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    session_id uuid        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    app_id     text        NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    host       text        NOT NULL CHECK (host = lower(host)),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX app_sessions_session_id_idx ON app_sessions (session_id);
CREATE INDEX app_sessions_app_id_idx     ON app_sessions (app_id);

CREATE TABLE login_codes (
    code_hash   bytea       PRIMARY KEY CHECK (octet_length(code_hash) = 32),
    session_id  uuid        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    app_id      text        NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    host        text        NOT NULL CHECK (host = lower(host)),
    bind_hash   bytea       NOT NULL CHECK (octet_length(bind_hash) = 32),
    rd          text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz
);
CREATE INDEX login_codes_session_id_idx ON login_codes (session_id);
CREATE INDEX login_codes_expires_at_idx ON login_codes (expires_at);
