-- Security review hardening (never edit applied migrations).

-- Bound the free-text values that end up inside the Platform-Assertion, so the assertion size has a
-- provable worst case (see assertion.MaxSize).
ALTER TABLE initiative_roles ADD CONSTRAINT initiative_roles_key_length CHECK (char_length(key) <= 32);
ALTER TABLE users ADD CONSTRAINT users_email_length CHECK (char_length(email) <= 254);
ALTER TABLE users ADD CONSTRAINT users_name_length CHECK (char_length(name) <= 200);

-- The runtime role only changes lifecycle columns of signing_keys: never kid, public_jwk or created_at.
REVOKE UPDATE ON signing_keys FROM platform_runtime;
GRANT UPDATE (state, activated_at, retired_at, removed_at, private_enc) ON signing_keys TO platform_runtime;
