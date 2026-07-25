-- Device push-token registry for Expo remote push notifications.
--
-- Why: task events (accept / clock-in / clock-out / complete / cancel) already
-- fan out to in-app rows + email via notify.Create. This table is the missing
-- input for the third channel — OS push — mapping an internal user to the Expo
-- push tokens of their devices. One user can have several devices, so the grain
-- is (user_id, expo_push_token) with the token globally unique: a token belongs
-- to exactly one user at a time, and re-registering it under a new user (device
-- hand-off / account switch) reassigns it via ON CONFLICT (expo_push_token).
--
-- Identity: user_id is the internal public.users.id UUID (S-20), FK with
-- ON DELETE CASCADE so a deleted user's tokens vanish with them. No email
-- identity column (S-20 / S-60).
--
-- Security (S-10 / S-13): new table ships with its control in the SAME
-- migration — RLS ENABLED with ZERO policies (default-deny). The anon/authed
-- PostgREST side door is closed; the Go backend (postgres role) is the only
-- writer, via POST /push/register and /push/unregister under dualAuth. No
-- policy is added — a policy here would open a client-direct path (S-01).
--
-- Shape: additive, new table, no backfill. Reversible:
--   DROP TABLE public.device_push_tokens;
-- Deploy order (S-21): apply this migration BEFORE deploying the Go build that
-- references the table.

CREATE TABLE IF NOT EXISTS public.device_push_tokens (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id         uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  expo_push_token text NOT NULL UNIQUE,
  platform        text,
  created_at      timestamp with time zone NOT NULL DEFAULT now(),
  last_seen_at    timestamp with time zone NOT NULL DEFAULT now()
);

-- SendPush looks tokens up by recipient on every task event — index the FK.
CREATE INDEX IF NOT EXISTS idx_device_push_tokens_user_id
  ON public.device_push_tokens USING btree (user_id);

-- Deny-all lock on the PostgREST side door (S-10). Zero policies by design.
ALTER TABLE public.device_push_tokens ENABLE ROW LEVEL SECURITY;

COMMENT ON TABLE public.device_push_tokens IS
  'Expo push tokens per user device. Written only by the Go backend (POST /push/register, /push/unregister); read by notify.SendPush. RLS deny-all (S-10).';
