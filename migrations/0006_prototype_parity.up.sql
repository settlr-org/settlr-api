ALTER TABLE users
  ADD COLUMN IF NOT EXISTS bank_qr_url TEXT,
  ADD COLUMN IF NOT EXISTS bank_name TEXT,
  ADD COLUMN IF NOT EXISTS payment_handle TEXT;

ALTER TABLE recurring_expenses DROP CONSTRAINT IF EXISTS recurring_expenses_frequency_check;
ALTER TABLE recurring_expenses ADD CONSTRAINT recurring_expenses_frequency_check
  CHECK (frequency IN ('DAILY','WEEKLY','MONTHLY','YEARLY'));

CREATE TABLE IF NOT EXISTS personal_budgets (
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  month DATE NOT NULL,
  amount BIGINT NOT NULL CHECK (amount >= 0),
  currency CHAR(3) NOT NULL DEFAULT 'NPR',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, month)
);
