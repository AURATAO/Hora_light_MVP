-- M3.2d round 4 (D-17 R-3): the armed approval question's own provider
-- message id, so a WhatsApp reply swiped on the question itself counts as
-- an answer to it. Forward-only, idempotent.
ALTER TABLE public.channel_conversations
  ADD COLUMN IF NOT EXISTS approval_question_pid text;
