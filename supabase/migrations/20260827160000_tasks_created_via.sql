-- Record which client path produced a task.
--
-- Why: the October round needs to count how many tasks are re-posts of an
-- earlier one ("Post again" / duplicate), which is the whole point of that
-- feature — recurring errands were the loudest piece of beta feedback. Nothing
-- on `tasks` carries any attribution today, so a duplicate and a hand-typed
-- task are indistinguishable after the fact.
--
--   'form'      -- Post Task's structured form, filled by hand ("Fill manually")
--   'ai_parse'  -- Post Task's free-text step, fields filled by POST /ai/parse-task
--   'duplicate' -- Post Task opened from "Post again" on a past task
--
-- Shape: additive and NULLABLE with no default, so it is safe to apply BEFORE
-- the Go build that writes it (S-21 expand step). Existing rows and every
-- client that sends no `created_via` — the web app, and the TestFlight build
-- currently in the field — land on NULL, which honestly means "unknown", not a
-- guessed-at 'form'. Count duplicates as `created_via = 'duplicate'`; treat
-- NULL as unattributed rather than folding it into any bucket.
--
-- Write-once at insert: POST /tasks sets it, PATCH /tasks/:id names its columns
-- explicitly and does not touch this one, so editing a task never rewrites how
-- it was created. Reversible:
-- `ALTER TABLE public.tasks DROP COLUMN created_via;`.
--
-- Security: existing table; RLS is already enabled with zero policies
-- (S-10 / D-02). No new surface, no policy added.

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS created_via text;

-- Mirrors tasks_removal_reason_check and task_gps_pings_source_check: the set
-- is closed, so keep it closed in the database as well as in the handler. NULL
-- is allowed explicitly — it is the value every pre-existing row has.
ALTER TABLE public.tasks DROP CONSTRAINT IF EXISTS tasks_created_via_check;
ALTER TABLE public.tasks ADD CONSTRAINT tasks_created_via_check
  CHECK (created_via IS NULL OR created_via = ANY (ARRAY['form'::text, 'ai_parse'::text, 'duplicate'::text]));

COMMENT ON COLUMN public.tasks.created_via IS
  'Which client path created this task: form (manual entry), ai_parse (free-text parser), duplicate (Post again from a past task). Set once by POST /tasks; never rewritten by an edit. NULL on rows created before the column existed and by clients that omit it (web).';
