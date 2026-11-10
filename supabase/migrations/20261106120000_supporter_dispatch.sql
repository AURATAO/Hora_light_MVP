-- Supporter push dispatch (D-24, Aura 2026-11-06). When a task goes live it
-- is pushed to available supporters who serve its borough; after 3 minutes
-- with no accept, to every other available supporter; after 5, ops are
-- emailed. All of it runs only with DISPATCH_PUSH_ENABLED=1.
--
-- WHAT THIS ADDS
--
--   profiles.dispatch_boroughs  the boroughs a supporter takes tasks in, as
--                               the server spells them ("Manhattan",
--                               "Brooklyn", "Queens", "the Bronx",
--                               "Staten Island"); empty = none chosen yet
--                               (round 2 still reaches them)
--   profiles.available_until    "I'm available" until this time; NULL or
--                               past = not available. Set to now + 4 h by
--                               the toggle, so it switches itself off.
--
--   task_dispatches             one row per dispatched task: the borough
--                               round 1 used, who round 1 reached, and when
--                               each step ran. The sweep reads the times, so
--                               widening and the ops email survive a
--                               restart or a deploy (D-20).
--
-- Deleted with the task (ON DELETE CASCADE). Supporter ids in
-- round1_user_ids are not foreign keys: a deleted supporter's id there
-- matches nobody.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): RLS ENABLED, no policies, no
-- client grants — the REVOKE is load-bearing against the schema's default
-- privileges. Go is the only reader and writer.
--
-- Invariant check after applying — all four CLAUDE.md Rule 3 queries: 1
-- still returns exactly the three deny-all policies; 2–4 return nothing.
--
-- Deploy order: apply BEFORE a build that runs with DISPATCH_PUSH_ENABLED=1.
-- With the flag off nothing reads these. With the flag on and no table, a
-- dispatch logs its error and does nothing (tasks still post; ops still get
-- the "New task posted" email).
--
-- Reversible:
--   DROP TABLE public.task_dispatches;
--   ALTER TABLE public.profiles DROP COLUMN dispatch_boroughs,
--                               DROP COLUMN available_until;

ALTER TABLE public.profiles
  ADD COLUMN IF NOT EXISTS dispatch_boroughs text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS available_until timestamptz;

CREATE TABLE IF NOT EXISTS public.task_dispatches (
  task_id          uuid PRIMARY KEY REFERENCES public.tasks(id) ON DELETE CASCADE,
  borough          text NOT NULL DEFAULT '',
  round1_at        timestamptz NOT NULL DEFAULT now(),
  round1_user_ids  uuid[] NOT NULL DEFAULT '{}',
  widened_at       timestamptz,
  widened_count    integer NOT NULL DEFAULT 0,
  ops_alerted_at   timestamptz,
  closed_at        timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now()
);

-- The sweep's scan: rows still waiting on a step.
CREATE INDEX IF NOT EXISTS task_dispatches_open_idx
  ON public.task_dispatches (round1_at) WHERE closed_at IS NULL;

ALTER TABLE public.task_dispatches ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON public.task_dispatches FROM anon, authenticated, PUBLIC;
