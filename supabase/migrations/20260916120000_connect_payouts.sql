-- Stripe Phase 3: paying the supporter.
--
-- Phases 1–2b took the requester's money: a hold at post, a capture at
-- completion, a balance charge for whatever the hold could not cover. All of
-- it landed on the PLATFORM's Stripe balance and stopped there. This migration
-- adds the far half of the loop — the connected account the money leaves to,
-- and the ledger of every transfer out.
--
-- WHAT THIS ADDS
--
--   users.stripe_account_id            the supporter's Connect Express account
--   users.stripe_payouts_enabled       status cache: can they be paid today?
--   users.stripe_details_submitted     status cache: did they finish the form?
--   users.stripe_requirements_due      status cache: what Stripe still wants
--   users.stripe_account_updated_at    when the cache was last refreshed
--   public.payouts                     one row per Transfer to a supporter
--
-- THE CACHE COLUMNS ARE A CACHE, NOT A RECORD. Stripe is the authority on
-- whether an account can receive money; these four columns exist so that
-- `GET /tasks/available` and the accept gate can answer "is this supporter
-- payable" without an API call on every request. They are written from exactly
-- two places — a status read that just talked to Stripe, and the
-- account.updated webhook — and anything that must be RIGHT rather than fast
-- re-reads the account. See payments_connect.go.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): payouts ships with its control in
-- THIS migration — RLS ENABLED, ZERO policies, and an explicit REVOKE against
-- the schema's ALTER DEFAULT PRIVILEGES (which silently grants anon and
-- authenticated full DML on every new table — see the long note in
-- 20260911120000). payouts is the second money table in this product and it is
-- the one a client-direct write would be most profitable against: an INSERT
-- here is an instruction to move money to a bank account. Deny-all, no
-- policies, Go only.
--
-- Invariant check after applying (all four from CLAUDE.md Rule 3 must still
-- give their expected answers; query 1 must still return exactly the three
-- deny-all policies and NOT mention payouts).
--
-- Reversible:
--   DROP TABLE public.payouts;
--   ALTER TABLE public.users
--     DROP COLUMN stripe_account_id, DROP COLUMN stripe_payouts_enabled,
--     DROP COLUMN stripe_details_submitted, DROP COLUMN stripe_requirements_due,
--     DROP COLUMN stripe_account_updated_at;
--
-- Deploy order (S-21): apply this BEFORE deploying the Go build that reads
-- these columns. The Go side gates payouts behind PAYMENTS_ENFORCED (default
-- off), so this migration is inert until that flag is flipped.

-- ── users: the connected account ───────────────────────────────────────────
--
-- One Stripe Express account per supporter, created lazily the first time they
-- START onboarding — not when they are approved as a supporter, and certainly
-- not at signup. Same reasoning as stripe_customer_id in 20260914120000: an
-- account created for someone who never onboards is a permanent record in
-- Stripe's database, and Connect accounts are heavier than Customers (they
-- carry KYC obligations and appear in the platform's Connect dashboard).
--
-- UNIQUE because the account.updated webhook arrives holding only an acct_… id
-- and has to find exactly one user. Two users sharing a connected account
-- would mean one person's bank details receiving another's earnings.

ALTER TABLE public.users
  ADD COLUMN IF NOT EXISTS stripe_account_id text;

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_stripe_account_id
  ON public.users USING btree (stripe_account_id)
  WHERE stripe_account_id IS NOT NULL;

COMMENT ON COLUMN public.users.stripe_account_id IS
  'Stripe Connect Express account (acct_…) this supporter is paid into. Created lazily when they start payout onboarding, never at signup. Written only by the Go backend (payments_connect.go).';

-- ── users: the status cache ────────────────────────────────────────────────
--
-- payouts_enabled defaults FALSE and that default is the whole safety
-- property: a supporter with no connected account, or one whose row predates
-- this migration, is not payable, so the accept gate refuses rather than
-- letting someone work a task we cannot pay them for. A default of true would
-- make every one of the existing accounts look payable.

ALTER TABLE public.users
  ADD COLUMN IF NOT EXISTS stripe_payouts_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS stripe_details_submitted boolean NOT NULL DEFAULT false,
  -- Stripe's `requirements.currently_due` verbatim, as a JSON array of field
  -- names. Stored rather than summarised to a boolean because the Earnings
  -- screen distinguishes "not started" from "in progress", and because the
  -- field names are what an ops person needs when a supporter asks why they
  -- are still blocked.
  ADD COLUMN IF NOT EXISTS stripe_requirements_due jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS stripe_account_updated_at timestamp with time zone;

COMMENT ON COLUMN public.users.stripe_payouts_enabled IS
  'CACHE of Stripe account.payouts_enabled. Authority is Stripe; this exists so the accept gate does not make an API call per request. Written by payments_connect.go and by the account.updated webhook only. Defaults false so an un-onboarded supporter is never treated as payable.';
COMMENT ON COLUMN public.users.stripe_requirements_due IS
  'CACHE of Stripe account.requirements.currently_due (JSON array of field names). Drives the not-started / in-progress / complete state machine on the Earnings screen.';

-- ── payouts ────────────────────────────────────────────────────────────────
--
-- One row per Stripe Transfer out to a supporter's connected account. The
-- money mirror of `payments`: that table records what the requester was
-- charged, this one records what the supporter was paid, and a completed task
-- with money on both sides has rows in both.
--
-- payment_id IS THE IDEMPOTENCY KEY, and it is UNIQUE rather than merely
-- indexed. A settlement funds its transfer from exactly one captured payment,
-- so "one payout per payment" is not a convention to be maintained by careful
-- code — it is a constraint the database enforces, and it is the thing that
-- makes a retried settle, a redelivered webhook and a double-tapped admin
-- retry all incapable of paying a supporter twice. Stripe's own idempotency
-- key is the second line of defence, not the first.
--
-- amount_cents is what the SUPPORTER receives, already net of any platform
-- cut. During beta the cut is zero (BillingConfig.ApplicationFeeBasisPoints),
-- so it equals time cost + reimbursed receipt.
--
-- NOTE ON THE PLATFORM CUT. With separate charges and transfers — a charge on
-- the platform, then a Transfer out — Stripe's application_fee_amount does not
-- apply; it is only valid on direct and destination charges. The platform's
-- cut here is simply the money that is NOT transferred, and it stays on the
-- platform balance. There is nothing to record on the charge side.

CREATE TABLE IF NOT EXISTS public.payouts (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  task_id            uuid NOT NULL REFERENCES public.tasks(id),
  supporter_id       uuid NOT NULL REFERENCES public.users(id),
  -- The captured payment funding this transfer. NOT NULL: a payout that
  -- cannot name the money it came from is unreconcilable.
  payment_id         uuid NOT NULL REFERENCES public.payments(id),
  amount_cents       integer NOT NULL CHECK (amount_cents > 0),
  stripe_transfer_id text UNIQUE,
  status             text NOT NULL CHECK (status IN ('pending','paid','failed')),
  -- How many times a transfer has been ATTEMPTED at Stripe. Drives the
  -- idempotency key, which must change between attempts: Stripe caches the
  -- FAILURE against a key for 24h, so a retry reusing the first key would be
  -- answered with the first attempt's error rather than actually retrying.
  -- Safe because a failed attempt created nothing — a successful one sets
  -- stripe_transfer_id, and retry refuses a row that has one.
  attempt_count      integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  -- Why a failure failed, and the shortfall flag. Free-form rather than
  -- columns because every field here is read by a human during an incident,
  -- not by a query.
  meta               jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at         timestamp with time zone NOT NULL DEFAULT now(),
  updated_at         timestamp with time zone NOT NULL DEFAULT now()
);

-- One payout per payment, forever. See the note above: this is the constraint
-- that makes double-paying a supporter impossible rather than unlikely.
CREATE UNIQUE INDEX IF NOT EXISTS uq_payouts_payment_id
  ON public.payouts USING btree (payment_id);

-- The Earnings list ("your recent transfers", newest first) and the ops view
-- of one task's money.
CREATE INDEX IF NOT EXISTS idx_payouts_supporter_created
  ON public.payouts USING btree (supporter_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_payouts_task_id
  ON public.payouts USING btree (task_id);

-- The ops queue: every transfer that needs a human. Partial, because the
-- healthy rows are the overwhelming majority and none of them belong here.
CREATE INDEX IF NOT EXISTS idx_payouts_failed
  ON public.payouts USING btree (created_at DESC)
  WHERE status = 'failed';

ALTER TABLE public.payouts ENABLE ROW LEVEL SECURITY;

-- Not boilerplate. This schema's ALTER DEFAULT PRIVILEGES grants anon and
-- authenticated `arwdDxtm` on every newly created table, so the CREATE TABLE
-- above just handed both roles INSERT on the payout ledger. Deny-all RLS stops
-- them today, but grants plus RLS is exactly the configuration that let nine
-- permissive policies sit exploitable for two months (CLAUDE.md, "Why the
-- query, and not a date"), and it makes invariant query 2 return rows — a
-- finding by the project's own rule.
REVOKE ALL ON TABLE public.payouts FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.payouts IS
  'Stripe Transfer ledger: one row per payment sent to a supporter''s Connect Express account. Written only by the Go backend (payments_payouts.go + POST /webhooks/stripe). RLS deny-all, zero policies (S-10) — a client-direct INSERT here would be an instruction to move money to a bank account.';
COMMENT ON COLUMN public.payouts.payment_id IS
  'The captured payments row funding this transfer. UNIQUE: one payout per payment, which is what makes a retried settle or a redelivered webhook incapable of double-paying.';
COMMENT ON COLUMN public.payouts.amount_cents IS
  'What the supporter receives, net of the platform cut (BillingConfig.ApplicationFeeBasisPoints, zero during beta). With separate charges and transfers the cut is simply money not transferred — Stripe application_fee_amount does not apply.';
COMMENT ON COLUMN public.payouts.attempt_count IS
  'Stripe transfer attempts. Feeds the idempotency key, which must differ per attempt because Stripe caches a failed call''s error against its key for 24h.';

-- ── payments.stripe_charge_id ──────────────────────────────────────────────
--
-- The charge behind a captured PaymentIntent, recorded at capture.
--
-- Needed because a Transfer's `source_transaction` takes a CHARGE id, not a
-- PaymentIntent id, and it is what makes the transfer succeed against a
-- platform balance that has not settled yet (see payments_payouts.go's header).
-- Without it every transfer is drawn on the platform's available balance and
-- fails with `balance_insufficient` — which is the default state of a test-mode
-- account and a frequent one in production, where card funds take days to
-- become available.
--
-- Stored rather than re-read from Stripe at transfer time: the capture call
-- already has the charge in hand when `latest_charge` is expanded, and a
-- payout path that needs an extra API call to find out where its money came
-- from has one more way to fail at the worst moment.
--
-- Nullable forever. Rows that were never captured have no charge, and rows
-- captured before this column existed will not be backfilled — the admin retry
-- path handles those by falling back to the platform balance, deliberately.

ALTER TABLE public.payments
  ADD COLUMN IF NOT EXISTS stripe_charge_id text;

CREATE INDEX IF NOT EXISTS idx_payments_stripe_charge_id
  ON public.payments USING btree (stripe_charge_id)
  WHERE stripe_charge_id IS NOT NULL;

COMMENT ON COLUMN public.payments.stripe_charge_id IS
  'The ch_… behind this captured intent, recorded at capture. Becomes the Transfer''s source_transaction so a supporter payout is funded by the requester''s actual charge rather than the platform''s available balance. NULL for uncaptured rows and for captures that predate this column.';
