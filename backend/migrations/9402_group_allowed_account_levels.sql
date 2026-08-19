ALTER TABLE groups
ADD COLUMN IF NOT EXISTS allowed_account_levels JSONB NOT NULL DEFAULT '[]'::jsonb;
