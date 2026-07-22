-- Add supporter_rejected_at to public.profiles.
--
-- Why: supporter_status is derived in Go only (S-05) as approved/applied/none.
-- Web's SupporterStatusBanner already renders a "rejected" state the backend
-- could never produce. This column is the missing input:
--   is_verified_supporter        -> "approved"
--   supporter_rejected_at IS NOT NULL -> "rejected"
--   supporter_applied_at  IS NOT NULL -> "applied"
--   otherwise                    -> "none"
--
-- Shape: additive, nullable, no default, no backfill (expand step only — S-21
-- deploy-order note: apply this migration BEFORE deploying the Go build that
-- selects the column). Reversible: `ALTER TABLE public.profiles DROP COLUMN
-- supporter_rejected_at;` — the only data lost is rejection timestamps set
-- after this ships.
--
-- Security: existing table; RLS is already enabled with zero policies
-- (S-10 / D-02). No new surface, no policy added.

ALTER TABLE public.profiles
  ADD COLUMN IF NOT EXISTS supporter_rejected_at timestamp with time zone;

COMMENT ON COLUMN public.profiles.supporter_rejected_at IS
  'Set by POST /ops/supporter-reject; cleared by POST /supporter/apply and POST /ops/supporter-approve. Input to the Go-side supporter_status derivation.';
