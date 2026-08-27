-- Revert Phase 3: recurring expenses + DIRECT groups
DROP TABLE IF EXISTS recurring_expenses;
ALTER TABLE groups DROP COLUMN IF EXISTS direct_key;
ALTER TABLE groups DROP CONSTRAINT groups_group_type_check;
ALTER TABLE groups ADD CONSTRAINT groups_group_type_check CHECK (group_type IN ('HOME','TRIP','COUPLE','OTHER'));
