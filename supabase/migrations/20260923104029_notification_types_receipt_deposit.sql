-- Build 13: the two payment notifications a person is owed.
--
-- Its own migration, for the reason every notification_type migration in this
-- project is its own file: a new enum value cannot be USED in the transaction
-- that adds it, so the value has to be committed separately from anything
-- that writes it.
--
--   RECEIPT            to the requester, from the same settlement path that
--                      captures — a charge can never happen without one.
--                      Itemized (base, minutes × rate, reimbursement, approved
--                      extensions, promo discount, total, card, released
--                      remainder). Also sent for a successful settle-balance
--                      charge.
--   PAYOUT_DEPOSITED   to the supporter, once per Stripe bank payout
--                      (payout.paid), naming the amount and the tasks it
--                      covered. Distinct from PAYOUT_SENT, which is the
--                      transfer leaving the platform; this is the bank saying
--                      yes.
--
-- Both are task-scoped (notifications.task_id is NOT NULL): a receipt belongs
-- to its task, and a deposit is filed against the most recent task it carried.
--
-- Reversible: enum values cannot be dropped in PostgreSQL. Reverting means
-- leaving the values in place and unused, which is inert.

ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'RECEIPT';
ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'PAYOUT_DEPOSITED';
