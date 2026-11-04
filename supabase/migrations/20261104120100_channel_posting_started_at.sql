-- A clock for a text booking in flight (audit 2026-10-30, gap B.2).
-- Forward-only, additive; RLS unchanged (deny-all, Go only — S-10).
--
-- posting_started_at is when the booking in flight was claimed (state
-- 'drafting'). updated_at could not serve: every inbound message moves it
-- (TouchConversation), so a claim stranded by a crash never looked old while
-- the person kept texting, and the 3-minute reclaim never fired.
--
-- Deploy order: apply BEFORE the build that reads it (GetConversation
-- selects it). Reversal: drop the index, then the column.
ALTER TABLE public.channel_conversations
  ADD COLUMN IF NOT EXISTS posting_started_at timestamptz;

-- A booking in flight right now gets its clock from its last update.
UPDATE public.channel_conversations
   SET posting_started_at = updated_at
 WHERE state = 'drafting' AND posting_started_at IS NULL;

-- The sweeper's scan: only threads in flight.
CREATE INDEX IF NOT EXISTS channel_conversations_posting_started_at_idx
  ON public.channel_conversations (posting_started_at)
  WHERE state = 'drafting';

COMMENT ON COLUMN public.channel_conversations.posting_started_at IS
  'When the text booking in flight was claimed (state drafting). NULL otherwise. Go only.';
