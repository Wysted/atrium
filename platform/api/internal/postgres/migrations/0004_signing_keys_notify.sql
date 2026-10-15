-- Replicas keep the active signer and the public key set in memory; any change to signing_keys
-- emits NOTIFY signing_keys_changed (payload: operation) so they reload without waiting for the refresh tick.
CREATE FUNCTION notify_signing_keys_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('signing_keys_changed', TG_OP);
    RETURN NULL;
END
$$;

CREATE TRIGGER signing_keys_changed
    AFTER INSERT OR UPDATE OR DELETE ON signing_keys
    FOR EACH STATEMENT EXECUTE FUNCTION notify_signing_keys_changed();
