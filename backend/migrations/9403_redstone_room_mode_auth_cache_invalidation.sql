-- A room-mode binding can change while api_keys.group_id remains the same
-- (for example when two rooms share an owner private group). In that case the
-- normal api_keys trigger cannot see the exact-room change, so invalidate the
-- API-key auth snapshot from the binding lifecycle itself.

CREATE OR REPLACE FUNCTION enqueue_redstone_room_mode_auth_cache_invalidation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_api_key_id BIGINT;
    raw_api_key TEXT;
BEGIN
    target_api_key_id := COALESCE(NEW.api_key_id, OLD.api_key_id);
    SELECT key INTO raw_api_key
    FROM api_keys
    WHERE id = target_api_key_id;

    PERFORM enqueue_auth_cache_invalidation(raw_api_key);

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_redstone_room_mode_auth_cache_invalidation
    ON redstone_api_key_room_bindings;
CREATE TRIGGER trg_redstone_room_mode_auth_cache_invalidation
AFTER INSERT OR UPDATE OR DELETE ON redstone_api_key_room_bindings
FOR EACH ROW EXECUTE FUNCTION enqueue_redstone_room_mode_auth_cache_invalidation();
