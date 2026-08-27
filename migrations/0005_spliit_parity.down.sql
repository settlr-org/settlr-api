DROP INDEX IF EXISTS idx_expenses_description_trgm;
DROP INDEX IF EXISTS idx_expenses_notes_trgm;
DROP INDEX IF EXISTS idx_activity_group_created;
ALTER TABLE expenses DROP COLUMN IF EXISTS original_amount;
ALTER TABLE expenses DROP COLUMN IF EXISTS original_currency;
ALTER TABLE expenses DROP COLUMN IF EXISTS conversion_rate;
ALTER TABLE categories DROP COLUMN IF EXISTS grouping;
ALTER TABLE groups DROP COLUMN IF EXISTS information;
-- pg_trgm extension kept (shared)
