-- Cancel a posted task by text (D-17, Arbiter 2026-10-24): the confirm step
-- needs its own state and the quoted task. Forward-only, additive; RLS on
-- channel_conversations is unchanged (deny-all, Go only — S-10).
-- Deploy order: apply BEFORE the build that reads pending_cancel
-- (GetConversation selects it; without the column every message fails).
-- The constraint name is the one 20261019120000 re-asserts. Reversal: drop
-- the column and re-assert the CHECK without 'awaiting_cancel' — after
-- moving any awaiting_cancel rows to 'idle'.
ALTER TABLE public.channel_conversations
  ADD COLUMN IF NOT EXISTS pending_cancel jsonb;

ALTER TABLE public.channel_conversations
  DROP CONSTRAINT IF EXISTS channel_conversations_state_check;
ALTER TABLE public.channel_conversations
  ADD CONSTRAINT channel_conversations_state_check CHECK (state IN (
    'idle', 'drafting', 'awaiting_name', 'awaiting_confirm', 'awaiting_setup',
    'awaiting_approval', 'awaiting_budget', 'awaiting_task_choice', 'handoff',
    'awaiting_cancel'));

COMMENT ON COLUMN public.channel_conversations.pending_cancel IS
  'Cancel-by-text in progress: the candidate tasks or the one quoted, and the charge quoted (cents). Go only.';
