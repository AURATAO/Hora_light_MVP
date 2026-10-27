-- M3.2d (D-17 R-1, R-3): four columns on channel_conversations, forward-only.
-- Idempotent: the staging branch already has them (applied 2026-10-22).

ALTER TABLE public.channel_conversations
  -- The task the armed approval request belongs to: the person's words
  -- around a YES/NO reach that supporter.
  ADD COLUMN IF NOT EXISTS approval_task_id         uuid,
  -- The armed question's own outbound stamp, and our latest outbound in
  -- the thread. Equal = nothing of ours since the question, so a reaction
  -- word ("Great", 👍) can only be about it.
  ADD COLUMN IF NOT EXISTS approval_question_out_at timestamp with time zone,
  ADD COLUMN IF NOT EXISTS last_outbound_at         timestamp with time zone,
  -- When a question of ours was last delivered; only moves forward. A
  -- booking YES sent before it is not an answer to it.
  ADD COLUMN IF NOT EXISTS question_shown_at        timestamp with time zone;
