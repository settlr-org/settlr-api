-- +goose Up
-- Accounts deleted before the delete flow really erased data were only anonymised: their wallet data, Google link,
-- notifications, tokens and payment details stayed behind (and a Google sign-in could revive them). Erase all of that
-- for every anonymised user. Shared group history stays, attributed to "Deleted User".
DELETE FROM transactions WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM planned_payments WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM loans WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM budgets WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM tags WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM accounts WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM exchange_rate_overrides WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM category_order WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM user_settings WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM personal_expenses WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM personal_budgets WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM notifications WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM notification_preferences WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM oauth_identities WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM email_verification_tokens WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM password_reset_tokens WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM friend_invites WHERE invited_by IN (SELECT id FROM users WHERE is_anonymous);
DELETE FROM categories WHERE is_system = false AND owner_id IN (SELECT id FROM users WHERE is_anonymous);
UPDATE users SET bank_qr_url = NULL, bank_name = NULL, payment_handle = NULL, email_verified_at = NULL
 WHERE is_anonymous;

-- +goose Down
-- Erased data cannot be restored.
SELECT 1;
