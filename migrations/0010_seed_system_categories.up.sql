-- Standard categories are shared defaults, available to every user.  Keep IDs
-- stable so existing expenses and exports can safely reference them.
INSERT INTO categories (id, name, icon, color, grouping, is_system) VALUES
  ('10000000-0000-0000-0000-000000000001', 'Food & Drink', 'utensils', '#D97706', 'Food and Drink', true),
  ('10000000-0000-0000-0000-000000000002', 'Groceries', 'shopping-cart', '#65A30D', 'Food and Drink', true),
  ('10000000-0000-0000-0000-000000000003', 'Transport', 'car', '#2563EB', 'Transportation', true),
  ('10000000-0000-0000-0000-000000000004', 'Travel', 'plane', '#0284C7', 'Transportation', true),
  ('10000000-0000-0000-0000-000000000005', 'Housing', 'home', '#7C3AED', 'Home', true),
  ('10000000-0000-0000-0000-000000000006', 'Utilities', 'bulb', '#0891B2', 'Utilities', true),
  ('10000000-0000-0000-0000-000000000007', 'Health', 'heart', '#E11D48', 'Life', true),
  ('10000000-0000-0000-0000-000000000008', 'Shopping', 'shopping', '#DB2777', 'Life', true),
  ('10000000-0000-0000-0000-000000000009', 'Entertainment', 'play-circle', '#9333EA', 'Entertainment', true),
  ('10000000-0000-0000-0000-000000000010', 'Education', 'book', '#0F766E', 'Life', true),
  ('10000000-0000-0000-0000-000000000011', 'Subscriptions', 'credit-card', '#4F46E5', 'Life', true),
  ('10000000-0000-0000-0000-000000000012', 'Other', 'tag', '#6B7280', 'Uncategorized', true)
ON CONFLICT DO NOTHING;
