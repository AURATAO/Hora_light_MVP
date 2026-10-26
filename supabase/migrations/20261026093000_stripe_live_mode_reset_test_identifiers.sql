-- Stripe live-mode switch: clear every TEST-mode Stripe identifier.
--
-- WHY. A Stripe customer and a Connect account exist in ONE mode. A `cus_…`
-- created with a test key does not exist to a live key: the API answers "No
-- such customer". Production ran PAYMENTS_ENFORCED on with test keys, so on
-- 2026-10-26 it held 93 non-sandbox users with a test-mode
-- `stripe_customer_id` and 3 with a test-mode `stripe_account_id`.
--
-- The server reads those columns before it creates anything
-- (stripeCustomerFor, createConnectAccount are get-or-create). Left in place,
-- the 93 cannot add a card and therefore cannot post a task, and the 3
-- supporters' Earnings screen fails reading a Connect account that is not
-- there. Cleared, the same paths mint fresh LIVE objects on first use — which
-- is exactly what a mode switch means. The code has no mode marker on these
-- columns and deliberately so: the id is whatever the configured key says it
-- is. So the fix is data, not code.
--
-- NOT KEPT. The old test ids are dropped rather than parked in a column: the
-- only use for them would be tracing a test-mode charge back to a user, and
-- `payments.requester_id` already does that. The cards behind them were test
-- cards (4242…); nothing real is lost, and each person adds a card again on
-- their next post.
--
-- NOT TOUCHED.
--   * Sandbox users (`is_sandbox`), one row today: the review account keeps
--     running on STRIPE_SANDBOX_SECRET_KEY, a test key, so its test-mode
--     customer is correct and must stay (D-15).
--   * `payments` and `payouts` history (test `pi_…`, `ch_…`, `tr_…`). It is
--     the record of what happened and is never replayed. Production had no
--     live hold when this was written — 15 captured, 9 canceled, 0 authorized
--     — so nothing in flight can be captured with the wrong key.
--
-- Deploy order (S-21): apply BEFORE, or as soon as possible after, the deploy
-- carrying the live STRIPE_SECRET_KEY. In between, a person who opens the card
-- sheet sees an error; no data is corrupted either way.
--
-- One-off: this is the 2026-10-26 production mode switch. On any database that
-- is still on test keys it clears test customers that would simply be recreated.

update public.users
   set stripe_customer_id = null
 where not is_sandbox
   and stripe_customer_id is not null;

update public.users
   set stripe_account_id = null,
       -- The Connect cache described an account that no longer exists in the
       -- configured mode. Reset to the "never onboarded" shape, which is the
       -- truth: a live account has to be created.
       stripe_payouts_enabled    = false,
       stripe_transfers_active   = false,
       stripe_details_submitted  = false,
       stripe_requirements_due   = '[]'::jsonb,
       stripe_account_updated_at = null
 where not is_sandbox
   and stripe_account_id is not null;
