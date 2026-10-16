-- Deleting a user's wallet data and, for account deletion, the rest of their personal data (internal/users).
-- Shared group history (expenses, splits, settlements, comments) is NOT touched: it belongs to the group and
-- stays attributed to the anonymised "Deleted User" row.

-- name: DeleteUserTransactions :execrows
DELETE FROM transactions WHERE user_id = $1;

-- name: DeleteUserPlannedPayments :execrows
DELETE FROM planned_payments WHERE user_id = $1;

-- name: DeleteUserLoans :execrows
DELETE FROM loans WHERE user_id = $1;

-- name: DeleteUserBudgets :execrows
DELETE FROM budgets WHERE user_id = $1;

-- name: DeleteUserTags :execrows
DELETE FROM tags WHERE user_id = $1;

-- name: DeleteUserAccounts :execrows
DELETE FROM accounts WHERE user_id = $1;

-- name: DeleteUserRateOverrides :execrows
DELETE FROM exchange_rate_overrides WHERE user_id = $1;

-- name: DeleteUserCategoryOrder :exec
DELETE FROM category_order WHERE user_id = $1;

-- name: DeleteUserSettings :exec
DELETE FROM user_settings WHERE user_id = $1;

-- name: DeleteUserLegacyPersonalExpenses :execrows
DELETE FROM personal_expenses WHERE user_id = $1;

-- name: DeleteUserLegacyPersonalBudgets :exec
DELETE FROM personal_budgets WHERE user_id = $1;

-- Categories the user created. Rows in other tables that point at them are set to NULL by their foreign keys.
-- name: DeleteUserOwnedCategories :execrows
DELETE FROM categories WHERE owner_id = $1 AND is_system = false;

-- name: DeleteUserNotifications :exec
DELETE FROM notifications WHERE user_id = $1;

-- name: DeleteUserNotificationPreferences :exec
DELETE FROM notification_preferences WHERE user_id = $1;

-- The Google link: without this a later Google sign-in would find the old (anonymised) account.
-- name: DeleteUserOAuthIdentities :exec
DELETE FROM oauth_identities WHERE user_id = $1;

-- name: DeleteUserEmailVerificationTokens :exec
DELETE FROM email_verification_tokens WHERE user_id = $1;

-- name: DeleteUserPasswordResetTokens :exec
DELETE FROM password_reset_tokens WHERE user_id = $1;

-- Sessions hold the device, IP and refresh-token hash.
-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteUserFriendInvites :exec
DELETE FROM friend_invites WHERE invited_by = $1;

-- Pending group invitations addressed to the user's e-mail address.
-- name: DeletePendingGroupInvitesForEmail :exec
DELETE FROM group_invites WHERE status = 'PENDING' AND lower(email) = lower($1);
