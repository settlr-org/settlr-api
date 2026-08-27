DROP TABLE IF EXISTS personal_budgets;
ALTER TABLE users
  DROP COLUMN IF EXISTS payment_handle,
  DROP COLUMN IF EXISTS bank_name,
  DROP COLUMN IF EXISTS bank_qr_url;
