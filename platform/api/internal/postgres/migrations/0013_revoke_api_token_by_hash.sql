-- Revocation by secret (contract block 6, "Non-browser clients"). platform_portal cannot read token_hash, and
-- PostgreSQL also demands SELECT on a column used in a WHERE, so the portal cannot find the token a pasted secret
-- belongs to. Like purge_dead_api_tokens (0012) this is a narrow SECURITY DEFINER function: the only way the
-- portal touches token_hash. It never returns the hash; it returns the id of the token it revoked, or NULL when
-- nothing matched (unknown, expired or already revoked), so the caller learns nothing else.
CREATE FUNCTION revoke_api_token_by_hash(p_hash bytea) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    v_id uuid;
BEGIN
    IF p_hash IS NULL OR octet_length(p_hash) <> 32 THEN
        RAISE EXCEPTION 'api token hash must be 32 bytes'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    UPDATE api_tokens SET revoked_at = now()
     WHERE token_hash = p_hash AND revoked_at IS NULL AND expires_at > now()
    RETURNING id INTO v_id;
    RETURN v_id;
END
$$;
REVOKE EXECUTE ON FUNCTION revoke_api_token_by_hash(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION revoke_api_token_by_hash(bytea) TO platform_portal;
