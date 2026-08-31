-- Spliit parity: group information, category grouping, pg_trgm, activity index
-- P1 C

-- Group information (rich text, markdown)
ALTER TABLE groups ADD COLUMN IF NOT EXISTS information TEXT;

-- Categories: add grouping (7 groupings from Spliit)
ALTER TABLE categories ADD COLUMN IF NOT EXISTS grouping TEXT DEFAULT 'Uncategorized';
-- Backfill existing 12 system categories
UPDATE categories SET grouping = CASE
  WHEN name IN ('General','Payment') THEN 'Uncategorized'
  WHEN name IN ('Games','Movies','Music','Sports') THEN 'Entertainment'
  WHEN name IN ('Dining Out','Groceries','Liquor') THEN 'Food and Drink'
  WHEN name IN ('Electronics','Furniture','Household Supplies','Maintenance','Mortgage','Pets','Rent','Services') THEN 'Home'
  WHEN name IN ('Childcare','Clothing','Donation','Education','Gifts','Insurance','Medical Expenses','Taxes') THEN 'Life'
  WHEN name IN ('Bicycle','Bus/Train','Car','Gas/Fuel','Hotel','Parking','Plane','Taxi') THEN 'Transportation'
  WHEN name IN ('Cleaning','Electricity','Heat/Gas','Trash','TV/Phone/Internet','Water') THEN 'Utilities'
  WHEN name = 'Food' THEN 'Food and Drink'
  WHEN name = 'Drinks' THEN 'Food and Drink'
  WHEN name = 'Transport' THEN 'Transportation'
  WHEN name = 'Travel' THEN 'Transportation'
  WHEN name = 'Entertainment' THEN 'Entertainment'
  WHEN name = 'Shopping' THEN 'Life'
  WHEN name = 'Rent' THEN 'Home'
  WHEN name = 'Utilities' THEN 'Utilities'
  WHEN name = 'Health' THEN 'Life'
  WHEN name = 'Education' THEN 'Life'
  WHEN name = 'Other' THEN 'Uncategorized'
  ELSE 'Uncategorized'
END WHERE grouping = 'Uncategorized' OR grouping IS NULL;

-- Ensure Uncategorized/General exists for default
INSERT INTO categories (id, name, grouping, is_system) VALUES
  ('00000000-0000-0000-0000-000000000000', 'General', 'Uncategorized', true)
ON CONFLICT (id) DO NOTHING;

-- pg_trgm for search (ILIKE)
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS idx_expenses_description_trgm ON expenses USING gin (description gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_expenses_notes_trgm ON expenses USING gin (notes gin_trgm_ops);

-- Activity pagination index
CREATE INDEX IF NOT EXISTS idx_activity_group_created ON activity_events(group_id, created_at DESC, id DESC);

-- Original amount fields for Frankfurter parity (keep existing exchange_rate/base_* as well)
ALTER TABLE expenses ADD COLUMN IF NOT EXISTS original_amount BIGINT;
ALTER TABLE expenses ADD COLUMN IF NOT EXISTS original_currency CHAR(3);
ALTER TABLE expenses ADD COLUMN IF NOT EXISTS conversion_rate NUMERIC(12,6);
-- Backfill from existing exchange_rate/base_amount where applicable
UPDATE expenses SET original_amount = amount, original_currency = currency, conversion_rate = exchange_rate WHERE original_amount IS NULL AND exchange_rate IS NOT NULL;
