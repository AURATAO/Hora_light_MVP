-- users.stripe_transfers_active: the precondition a Transfer is actually refused on.
--
-- WHAT HAPPENED (2026-09-22, test mode, acct_1UGK5JJfBq5lpcyd).
--
-- The accept gate keyed on stripe_payouts_enabled alone. At 21:54:54 UTC the
-- account.updated webhook said payouts_enabled=true, details_submitted=true,
-- nothing due — and the v1 Account object's capabilities.transfers said
-- "active" too. The cache mirrored all of it faithfully. The supporter
-- accepted a task at 21:59:38. At 22:01:59 the Transfer for it was refused:
-- insufficient_capabilities_for_transfer.
--
-- The v1 Account was not the view the Transfer was checked against. This
-- platform has Accounts v2 enabled, and on the v2 view of the same account
-- (GET /v2/core/accounts/{id}?include=configuration.recipient) the recipient
-- capability configuration.recipient.capabilities.stripe_balance.stripe_transfers
-- was — and still is — "restricted" (requirements_past_due), while v1 says
-- "active" with an empty currently_due. The sibling account that HAS received
-- transfers is "active" on both views.
--
-- So payouts_enabled ("the account can reach a bank") was never the question.
-- "The platform can reach the account" is, and it has two answers that can
-- disagree. This column caches the conjunction the Go side computes
-- (payments_connect.go, transfersActiveFor): v1 capabilities.transfers is
-- active AND, when the v2 view is readable, its stripe_transfers capability
-- is active. The accept gate and the admin retry require it together with
-- stripe_payouts_enabled.
--
-- DEFAULT FALSE, NO BACKFILL FROM payouts_enabled. Seeding it from the column
-- that just proved insufficient would rebuild the bug. It is refreshed from
-- Stripe by the same three paths that refresh the rest of the cache (Earnings
-- view, onboarding return, account.updated), and a one-off refresh of every
-- user with a stripe_account_id is run as part of this change.

ALTER TABLE public.users
  ADD COLUMN IF NOT EXISTS stripe_transfers_active boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN public.users.stripe_transfers_active IS
  'CACHE: the platform can send a Transfer to this account — v1 capabilities.transfers is active AND the v2 recipient stripe_transfers capability is active (payments_connect.go transfersActiveFor). The accept gate requires this AND stripe_payouts_enabled. Defaults false; refreshed from Stripe, never derived from payouts_enabled.';

COMMENT ON COLUMN public.users.stripe_payouts_enabled IS
  'CACHE of Stripe account.payouts_enabled: the account can reach a BANK. Not sufficient on its own — a transfer was refused on 2026-09-22 with this true. The gate reads it together with stripe_transfers_active. Written by payments_connect.go and by the account.updated webhook only.';
