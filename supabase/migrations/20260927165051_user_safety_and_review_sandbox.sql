-- App Store submission: user safety (Guideline 1.2) and the review sandbox.
--
-- WHAT THIS ADDS
--
--   public.user_reports   one row per "Report user": who reported whom, on
--                         which task, under which preset reason (a closed set
--                         owned by the Go backend — server/safety.go), and
--                         whether ops has dealt with it
--   public.user_blocks    one row per "Block user". Directional as stored (who
--                         blocked whom, for the audit trail) and MUTUAL as
--                         enforced: every read in Go asks "is there a row in
--                         either direction", so a block stops both people
--                         being matched and both people messaging
--   users.is_sandbox      the App Review account. Its tasks live in a closed
--                         partition — invisible to and un-acceptable by every
--                         other account, and it sees only sandbox tasks — and
--                         every Stripe call made for it uses TEST keys, whatever
--                         the platform's live/test posture. See
--                         server/sandbox.go. Set by the review-login seed, never
--                         by a client; durable on the row rather than derived
--                         from REVIEW_ACCOUNT_EMAIL, because the Stripe objects
--                         it owns are test-mode objects for ever, whether or not
--                         the review env vars are still set.
--   profiles.terms_accepted_at
--                         when the person agreed to the Terms of Use and
--                         Privacy Policy. Both clients have always gated
--                         sign-in on a consent checkbox, but nothing recorded
--                         the answer; the clients now PATCH it once after
--                         sign-in, and the server stamps it once.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): both tables ship with their
-- control in THIS migration — RLS ENABLED, ZERO policies, and an explicit
-- REVOKE against the schema's ALTER DEFAULT PRIVILEGES (see the note in
-- 20260911120000 for why the REVOKE is load-bearing). A client-direct INSERT
-- into user_blocks would let anyone make themselves unmatchable to anyone, and
-- a client-direct read of user_reports would tell a reported user who reported
-- them. Deny-all, no policies, Go only.
--
-- Invariant check after applying (CLAUDE.md Rule 3, all four queries): query 1
-- still returns exactly the three deny-all policies and names neither table;
-- 2–4 return nothing.
--
-- Reversible:
--   DROP TABLE public.user_blocks;
--   DROP TABLE public.user_reports;
--   ALTER TABLE public.users DROP COLUMN is_sandbox;
--   ALTER TABLE public.profiles DROP COLUMN terms_accepted_at;
--
-- Deploy order (S-21): apply BEFORE the Go build that reads these.

-- ── users.is_sandbox ───────────────────────────────────────────────────────

ALTER TABLE public.users
  ADD COLUMN IF NOT EXISTS is_sandbox boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN public.users.is_sandbox IS
  'The App Review account. Closed task partition + Stripe TEST keys for every call made for it (server/sandbox.go). Set only by the Go review-login seed.';

-- ── profiles.terms_accepted_at ─────────────────────────────────────────────

ALTER TABLE public.profiles
  ADD COLUMN IF NOT EXISTS terms_accepted_at timestamp with time zone;

COMMENT ON COLUMN public.profiles.terms_accepted_at IS
  'First time the person agreed to https://www.my-hora.com/terms and /privacy. Stamped once by PATCH /profile {terms_accepted: true}; never cleared.';

-- ── user_reports ───────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.user_reports (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  reporter_id   uuid NOT NULL REFERENCES public.users(id),
  reported_id   uuid NOT NULL REFERENCES public.users(id),
  -- The task the two people met on. Every report surface (chat, task detail)
  -- is task-scoped, and ops needs the task to judge the report.
  task_id       uuid NOT NULL REFERENCES public.tasks(id),
  -- A preset code from server/safety.go reportReasons. Validated there; not
  -- mirrored as a CHECK so adding a preset is a Go change only.
  reason_code   text NOT NULL CHECK (reason_code <> ''),
  -- Optional free text, for ops only. Never shown to the reported user.
  details       text,
  created_at    timestamp with time zone NOT NULL DEFAULT now(),
  resolved_at   timestamp with time zone,
  resolved_by   uuid REFERENCES public.users(id),
  resolution    text,

  CONSTRAINT user_reports_not_self CHECK (reporter_id <> reported_id),
  CONSTRAINT user_reports_resolved_shape CHECK (
    (resolved_at IS NULL) = (resolved_by IS NULL)
  )
);

CREATE INDEX IF NOT EXISTS idx_user_reports_open
  ON public.user_reports USING btree (created_at DESC) WHERE resolved_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_reports_reported
  ON public.user_reports USING btree (reported_id);

ALTER TABLE public.user_reports ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.user_reports FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.user_reports IS
  'User-to-user reports (App Store Guideline 1.2). Written by POST /safety/report, read by ops via GET /admin/reports. RLS deny-all, zero policies (S-10).';

-- ── user_blocks ────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.user_blocks (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  blocker_id  uuid NOT NULL REFERENCES public.users(id),
  blocked_id  uuid NOT NULL REFERENCES public.users(id),
  -- Where the block was made from. Informational only; a block is between two
  -- people, not two people on one task.
  task_id     uuid REFERENCES public.tasks(id),
  created_at  timestamp with time zone NOT NULL DEFAULT now(),

  CONSTRAINT user_blocks_not_self CHECK (blocker_id <> blocked_id)
);

-- One row per direction; blocking twice is a no-op (ON CONFLICT DO NOTHING).
CREATE UNIQUE INDEX IF NOT EXISTS uq_user_blocks_pair
  ON public.user_blocks USING btree (blocker_id, blocked_id);
-- The reverse direction, for the mutual "either way" lookup.
CREATE INDEX IF NOT EXISTS idx_user_blocks_blocked
  ON public.user_blocks USING btree (blocked_id, blocker_id);

ALTER TABLE public.user_blocks ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.user_blocks FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.user_blocks IS
  'User blocks (App Store Guideline 1.2). Stored directionally, enforced mutually by the Go backend: a blocked pair cannot be matched on a task or exchange messages. RLS deny-all, zero policies (S-10).';

NOTIFY pgrst, 'reload schema';
