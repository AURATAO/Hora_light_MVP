-- Traction 3 (Aug 24–28) post-task questionnaire.
--
-- Path (a): extend public.reviews rather than adding a second store. That
-- table already carries (task_id, reviewer_id, supporter_id, stars) and is the
-- single source the public supporter profile reads from
-- (GET /profiles/:id/reviews -> listProfileReviews), so a Traction 3 star
-- rating lands in the same place a classic review does and aggregates with it.
-- A parallel task_reviews table would have split that read path in two.
--
-- RLS on this table stays enabled with zero policies (deny-all). Nothing here
-- grants a client anything: the Go backend connects as `postgres` and remains
-- the only authorization layer (CLAUDE.md rules 1-4, D-02).

-- Questionnaire answers. All nullable: a classic review fills none of them, a
-- supporter-submitted row fills only the last three, and a Traction 3
-- requester row fills all four.
--
-- open_feedback is deliberately NOT `comment`. `comment` is the requester's
-- public note about the supporter and renders on their profile; open_feedback
-- answers "what should we improve before launch" — product feedback for the
-- HO:RA team that must never surface as a supporter's public review text.
-- rater_role carries `default 'requester'` for one reason: it lets the CURRENTLY
-- DEPLOYED backend keep inserting reviews after this migration lands and before
-- the new code ships. That INSERT names no rater_role, and without a default the
-- NOT NULL below would 500 every classic review submission until the deploy
-- caught up. The new handler always sets the column explicitly, so the default
-- is never what a new row relies on — it only removes the deploy-order window.
alter table public.reviews
  add column if not exists rater_role      text default 'requester',
  add column if not exists ease_rating     text,
  add column if not exists would_use_again text,
  add column if not exists open_feedback   text;

-- Every row written so far came from the requester-only endpoint, so the
-- backfill is unambiguous.
update public.reviews set rater_role = 'requester' where rater_role is null;
alter table public.reviews alter column rater_role set not null;

alter table public.reviews
  add constraint reviews_rater_role_check
  check (rater_role in ('requester', 'supporter'));

-- Slugs, never display labels — the labels live client-side only.
alter table public.reviews
  add constraint reviews_ease_rating_check
  check (ease_rating is null or ease_rating in
    ('very_easy', 'easy', 'neutral', 'difficult', 'very_difficult'));

alter table public.reviews
  add constraint reviews_would_use_again_check
  check (would_use_again is null or would_use_again in
    ('yes', 'maybe_task', 'maybe_cost', 'no'));

-- A supporter-submitted row rates nobody: supporter_id and stars stay null,
-- which the existing nullable columns and the `stars between 1 and 5` check
-- (null passes) already allow. What must hold is the pairing.
alter table public.reviews
  add constraint reviews_supporter_row_rates_nobody
  check (rater_role <> 'supporter' or (supporter_id is null and stars is null));

-- One review per person per task, replacing the old one-review-per-TASK rule
-- enforced in Go. That rule predates the supporter questionnaire and would
-- have made the requester's review block the supporter's on the same task.
-- No duplicates can exist to violate this: the old rule was stricter.
alter table public.reviews
  add constraint reviews_task_rater_unique unique (task_id, reviewer_id);
