-- Stripe Phase 1: the payments ledger, the task columns settlement needs, and
-- the shopping-budget cap the product has been advertising without enforcing.
--
-- Nothing here charges anyone. Phase 1 ships the data model and a payments
-- service that is callable but wired into no task flow; Phase 2 wires pre-auth
-- at post and capture at completion. The schema lands first so that when those
-- flows arrive they are writing to columns that already exist in production.
--
-- WHAT THIS ADDS
--
--   public.payments                 one row per Stripe PaymentIntent
--   public.stripe_webhook_events    delivered event ids, for idempotency
--   tasks.shopping_budget_approved_cents
--   tasks.auto_extend_consent
--   tasks.receipt_amount_cents
--   tasks.receipt_photo_url
--   tasks_prepay_amount_cents_cap   CHECK (<= 3000), NOT VALID — see below
--
-- Security (S-10 / S-13): both new tables ship with their control in THIS
-- migration — RLS ENABLED, ZERO policies, no grants to anon/authenticated.
-- That is deny-all: the PostgREST side door is shut and the Go backend
-- (postgres role, bypasses RLS) is the only reader or writer. A policy on
-- either table would open a client-direct path and violate S-01 / Rule 1.
-- payments is the most sensitive table in the product — it is the one place a
-- client-direct read would expose what someone was charged — so this is not
-- boilerplate here.
--
-- Reversible:
--   ALTER TABLE public.tasks DROP CONSTRAINT tasks_prepay_amount_cents_cap;
--   ALTER TABLE public.tasks
--     DROP COLUMN shopping_budget_approved_cents, DROP COLUMN auto_extend_consent,
--     DROP COLUMN receipt_amount_cents, DROP COLUMN receipt_photo_url;
--   DROP TABLE public.stripe_webhook_events;
--   DROP TABLE public.payments;
--
-- Deploy order (S-21): apply this BEFORE deploying the Go build that
-- references these tables.

-- ── payments ───────────────────────────────────────────────────────────────
--
-- One row per PaymentIntent, which is one row per task in Phase 2's happy path.
-- kind is a single value today and exists so the Phase 2 budget-increase flow
-- ('budget_increase') adds a value rather than a table.
--
-- Money columns are nullable on purpose and fill in as the intent moves: a row
-- born 'requires_auth' knows only what it intends to hold, learns
-- authorized_cents when the hold lands, and learns captured_cents /
-- time_cost_cents / shopping_receipt_cents at settlement. A captured row
-- therefore carries the full split of what the requester paid and why, which
-- is what a dispute needs and what an amount-only column could not answer.

CREATE TABLE IF NOT EXISTS public.payments (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  task_id                  uuid NOT NULL REFERENCES public.tasks(id),
  requester_id             uuid NOT NULL REFERENCES public.users(id),
  kind                     text NOT NULL CHECK (kind IN ('task_payment')),
  stripe_payment_intent_id text UNIQUE,
  status                   text NOT NULL CHECK (
                             status IN ('requires_auth','authorized','captured','canceled','failed')
                           ),
  authorized_cents         integer CHECK (authorized_cents       IS NULL OR authorized_cents       >= 0),
  captured_cents           integer CHECK (captured_cents         IS NULL OR captured_cents         >= 0),
  time_cost_cents          integer CHECK (time_cost_cents        IS NULL OR time_cost_cents        >= 0),
  shopping_receipt_cents   integer CHECK (shopping_receipt_cents IS NULL OR shopping_receipt_cents >= 0),
  created_at               timestamp with time zone NOT NULL DEFAULT now(),
  updated_at               timestamp with time zone NOT NULL DEFAULT now()
);

-- The webhook arrives knowing only the PaymentIntent id and has to find its
-- row on every event; the UNIQUE above already indexes that. These two serve
-- the other direction — the task page and the ops feed, which look up by task
-- and by requester.
CREATE INDEX IF NOT EXISTS idx_payments_task_id      ON public.payments USING btree (task_id);
CREATE INDEX IF NOT EXISTS idx_payments_requester_id ON public.payments USING btree (requester_id);

-- At most one live payment per task per kind. A duplicate pre-auth means two
-- holds on one card for one task, which the requester sees on their statement
-- and nobody reconciles; a partial unique index is the only thing that makes
-- a retry that races itself impossible rather than merely unlikely.
CREATE UNIQUE INDEX IF NOT EXISTS uq_payments_task_kind_live
  ON public.payments USING btree (task_id, kind)
  WHERE status IN ('requires_auth','authorized');

ALTER TABLE public.payments ENABLE ROW LEVEL SECURITY;

-- RLS alone is NOT enough here, and this is the exact trap CLAUDE.md Rule 3
-- was written about.
--
-- This project's `public` schema carries ALTER DEFAULT PRIVILEGES granting
-- `arwdDxtm` — every DML privilege — to anon and authenticated on every new
-- table, from both the `postgres` and `supabase_admin` grantors. Verified:
--
--   select pg_get_userbyid(defaclrole), defaclacl::text from pg_default_acl d
--   join pg_namespace n on n.oid = d.defaclnamespace
--   where n.nspname='public' and d.defaclobjtype='r';
--   -> {postgres=arwdDxtm/postgres,anon=arwdDxtm/postgres,
--       authenticated=arwdDxtm/postgres,service_role=arwdDxtm/postgres}
--
-- So `CREATE TABLE` above silently handed anon and authenticated SELECT,
-- INSERT, UPDATE and DELETE on the payments ledger. Deny-all RLS does stop
-- them today — zero policies means nothing is permitted regardless of grants —
-- but that is one `CREATE POLICY` away from a live client-direct path, and it
-- is precisely the RLS-on-plus-grants configuration that let nine permissive
-- policies sit exploitable for two months (see CLAUDE.md, "Why the query, and
-- not a date"). It also makes invariant query 2 return rows, which is a
-- finding by the project's own rule.
--
-- 20260910160000_revoke_residual_client_grants.sql had to do exactly this for
-- four earlier tables for the same reason. The durable fix is to change the
-- default privileges themselves so no future table needs this paragraph; that
-- is a schema-wide change and belongs in its own migration with its own
-- decision record.
REVOKE ALL ON TABLE public.payments FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.payments IS
  'Stripe PaymentIntent ledger, one row per task payment. Written only by the Go backend (payments.go + POST /webhooks/stripe). RLS deny-all, zero policies (S-10) — a client-direct read here would expose charge history.';

-- ── stripe_webhook_events ──────────────────────────────────────────────────
--
-- Stripe guarantees at-least-once delivery and retries for days on a non-2xx,
-- so the same event id WILL arrive twice. Without this table a replayed
-- payment_intent.succeeded re-runs whatever that handler does; with it, the
-- INSERT ... ON CONFLICT DO NOTHING at the top of the handler returns zero
-- rows and the duplicate is acknowledged and dropped.
--
-- The event id is the primary key rather than a surrogate: the thing being
-- deduplicated IS the identity.

CREATE TABLE IF NOT EXISTS public.stripe_webhook_events (
  event_id     text PRIMARY KEY,
  event_type   text NOT NULL,
  received_at  timestamp with time zone NOT NULL DEFAULT now(),
  processed_at timestamp with time zone
);

ALTER TABLE public.stripe_webhook_events ENABLE ROW LEVEL SECURITY;

-- Same default-privileges revoke as payments above, same reasoning.
REVOKE ALL ON TABLE public.stripe_webhook_events FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.stripe_webhook_events IS
  'Idempotency ledger for POST /webhooks/stripe. event_id is Stripe''s evt_… id; a second delivery of the same id is acknowledged and ignored. RLS deny-all (S-10).';

-- ── tasks: settlement columns ──────────────────────────────────────────────

ALTER TABLE public.tasks
  -- The approved ceiling, which starts as what the requester set at post and
  -- is what an approved budget-increase raises (Phase 2). prepay_amount_cents
  -- stays the requester's ORIGINAL ask; reimbursement is checked against this
  -- one. Keeping them separate is what makes "was this overage approved, and
  -- when" answerable after the fact.
  ADD COLUMN IF NOT EXISTS shopping_budget_approved_cents integer,

  -- Consent, captured at post, for the supporter to run up to
  -- BillingConfig.AutoExtendMinutes past the estimate without re-asking.
  -- Defaults true to match the behaviour every existing task already had:
  -- overtime has always billed without a per-task consent gate, so defaulting
  -- false would retroactively mark 97 live tasks as having refused something
  -- they were never asked.
  ADD COLUMN IF NOT EXISTS auto_extend_consent boolean NOT NULL DEFAULT true,

  -- Settlement inputs, written at completion: what the receipt actually said
  -- and the photo backing it. Reimbursement is the verified amount, capped at
  -- shopping_budget_approved_cents + the $5 tolerance.
  ADD COLUMN IF NOT EXISTS receipt_amount_cents integer
    CHECK (receipt_amount_cents IS NULL OR receipt_amount_cents >= 0),
  ADD COLUMN IF NOT EXISTS receipt_photo_url text;

-- Existing rows: the approved budget is what was asked for at post. Only
-- touches rows where the column is still NULL, so re-running is a no-op.
UPDATE public.tasks
   SET shopping_budget_approved_cents = prepay_amount_cents
 WHERE shopping_budget_approved_cents IS NULL;

COMMENT ON COLUMN public.tasks.shopping_budget_approved_cents IS
  'Currently-approved shopping ceiling. Starts equal to prepay_amount_cents; raised only by an approved budget-increase request. Reimbursement cap = this + BillingConfig.OverageToleranceCents.';
COMMENT ON COLUMN public.tasks.auto_extend_consent IS
  'Requester consented at post to the supporter running up to BillingConfig.AutoExtendMinutes past the estimate without a fresh approval.';

-- ── the $30 shopping cap, finally enforced ─────────────────────────────────
--
-- Both clients have told requesters "Purchase cap: $30 max per task" since the
-- beta notice shipped (app/src/components/BetaModal.jsx,
-- mobile/src/lib/beta-notice.ts) while nothing anywhere checked it. Go now
-- rejects an over-cap budget on create and update; this constraint is the
-- backstop for anything that reaches the table another way.
--
-- NOT VALID, deliberately. Twelve of the ninety-seven existing tasks carry
-- budgets above the cap, up to $100 — all of them cancelled, completed or
-- removed, none open. A validating constraint would fail to apply against
-- them, and the alternative — an UPDATE clamping them to $30 — would rewrite
-- the recorded budget on settled tasks to a number the requester never agreed
-- to. NOT VALID enforces the cap on every INSERT and UPDATE from here on and
-- leaves closed history exactly as it was transacted, which is the correct
-- treatment of a financial record.
--
-- To validate later, once those rows have aged out of anything that matters:
--   ALTER TABLE public.tasks VALIDATE CONSTRAINT tasks_prepay_amount_cents_cap;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'public.tasks'::regclass
      AND conname  = 'tasks_prepay_amount_cents_cap'
  ) THEN
    ALTER TABLE public.tasks
      ADD CONSTRAINT tasks_prepay_amount_cents_cap
      CHECK (prepay_amount_cents <= 3000) NOT VALID;
  END IF;
END
$$;

-- The same cap on the approved ceiling would be wrong: an approved budget
-- increase is exactly the mechanism for exceeding the original ask, and Phase 2
-- bounds it per-request rather than absolutely.
