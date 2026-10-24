-- Hand-off timeout (D-17, Arbiter 2026-10-24 22:39): a handed-off chat with
-- no ops activity for 10 minutes goes back to the bot. handoff_activity_at is
-- when ops last acted (the hand-off itself, or an ops message); the Go
-- backend manages it. Forward-only, additive; RLS unchanged (deny-all, Go
-- only — S-10).
-- Deploy order: apply BEFORE the build that reads handoff_activity_at
-- (GetConversation selects it). Reversal: drop the column.
ALTER TABLE public.channel_conversations
  ADD COLUMN IF NOT EXISTS handoff_activity_at timestamptz;

-- Threads already handed off start their clock at their last update.
UPDATE public.channel_conversations
   SET handoff_activity_at = updated_at
 WHERE state = 'handoff' AND handoff_activity_at IS NULL;

COMMENT ON COLUMN public.channel_conversations.handoff_activity_at IS
  'When ops last acted on a handed-off thread (hand-off or ops message). NULL when not handed off. Go only.';
