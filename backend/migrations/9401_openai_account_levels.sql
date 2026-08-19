ALTER TABLE accounts ADD COLUMN IF NOT EXISTS account_level VARCHAR(64) NOT NULL DEFAULT 'unknown';
CREATE INDEX IF NOT EXISTS idx_accounts_platform_account_level ON accounts (platform, account_level);

UPDATE accounts
SET account_level = CASE
    WHEN lower(regexp_replace(coalesce(credentials->>'plan_type', credentials->>'chatgpt_plan_type', credentials->>'subscription_plan', ''), '[ _-]', '', 'g')) LIKE 'plus%' THEN 'plus'
    WHEN lower(regexp_replace(coalesce(credentials->>'plan_type', credentials->>'chatgpt_plan_type', credentials->>'subscription_plan', ''), '[ _-]', '', 'g')) LIKE 'pro%' THEN 'pro'
    WHEN lower(regexp_replace(coalesce(credentials->>'plan_type', credentials->>'chatgpt_plan_type', credentials->>'subscription_plan', ''), '[ _-]', '', 'g')) LIKE 'team%' THEN 'team'
    WHEN lower(regexp_replace(coalesce(credentials->>'plan_type', credentials->>'chatgpt_plan_type', credentials->>'subscription_plan', ''), '[ _-]', '', 'g')) IN ('free', 'chatgptfree') THEN 'free'
    WHEN lower(regexp_replace(coalesce(credentials->>'plan_type', credentials->>'chatgpt_plan_type', credentials->>'subscription_plan', ''), '[ _-]', '', 'g')) IN ('k12', 'chatgptk12') THEN 'k12'
    ELSE account_level
END
WHERE platform = 'openai' AND account_level = 'unknown';
