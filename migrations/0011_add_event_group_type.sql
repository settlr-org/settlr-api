-- +goose Up
-- Allow EVENT group type (shown in mobile UI) alongside existing types
ALTER TABLE groups DROP CONSTRAINT groups_group_type_check;
ALTER TABLE groups ADD CONSTRAINT groups_group_type_check CHECK (group_type IN ('HOME','TRIP','COUPLE','EVENT','OTHER','DIRECT'));

-- +goose Down
ALTER TABLE groups DROP CONSTRAINT groups_group_type_check;
ALTER TABLE groups ADD CONSTRAINT groups_group_type_check CHECK (group_type IN ('HOME','TRIP','COUPLE','OTHER','DIRECT'));
