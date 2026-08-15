ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS owner_user_id BIGINT;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.tables
        WHERE table_schema = 'public'
          AND table_name = 'redstone_user_controlled_accounts'
    ) THEN
        UPDATE accounts AS a
        SET owner_user_id = r.owner_user_id
        FROM redstone_user_controlled_accounts AS r
        WHERE r.account_id = a.id
          AND (a.owner_user_id IS NULL OR a.owner_user_id <> r.owner_user_id);
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_redstone_guard_user_controlled_account_runtime
    ON accounts;
DROP TRIGGER IF EXISTS trg_redstone_reject_user_controlled_account_group
    ON account_groups;
DROP TRIGGER IF EXISTS trg_redstone_verify_user_controlled_account
    ON redstone_user_controlled_accounts;

DROP FUNCTION IF EXISTS redstone_guard_user_controlled_account_runtime();
DROP FUNCTION IF EXISTS redstone_reject_user_controlled_account_group();
DROP FUNCTION IF EXISTS redstone_verify_user_controlled_account();

DO $$
DECLARE
    owner_fk_name TEXT;
BEGIN
    FOR owner_fk_name IN
        SELECT DISTINCT c.conname
        FROM pg_constraint AS c
        JOIN pg_class AS t
          ON t.oid = c.conrelid
        JOIN pg_namespace AS n
          ON n.oid = t.relnamespace
        JOIN pg_attribute AS a
          ON a.attrelid = t.oid
         AND a.attnum = ANY (c.conkey)
        WHERE c.contype = 'f'
          AND n.nspname = 'public'
          AND t.relname = 'accounts'
          AND a.attname = 'owner_user_id'
    LOOP
        EXECUTE format('ALTER TABLE accounts DROP CONSTRAINT %I', owner_fk_name);
    END LOOP;
END
$$;

ALTER TABLE accounts
    ADD CONSTRAINT accounts_owner_user_id_fkey
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS idx_accounts_owner_user_id_created_at
    ON accounts (owner_user_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;
