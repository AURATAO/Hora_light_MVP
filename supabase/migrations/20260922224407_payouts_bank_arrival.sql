-- payouts: when the money reached the BANK, not just the connected account.
--
-- A payouts row is 'paid' the moment Stripe accepts the Transfer, which moves
-- money from the platform balance to the supporter's connected-account
-- balance. Their bank sees it later, on Stripe's payout schedule, as a
-- connected-account Payout (po_…) that sweeps the balance. Until now the
-- Earnings screen could not tell the two apart, so "Earned all time" said
-- "paid into your bank" about money that was still in transit, and a FAILED
-- transfer rendered the same as a paid one.
--
-- Three columns:
--
--   stripe_destination_payment  the py_… the Transfer created on the connected
--                               account. It is the join key between our row
--                               and Stripe's payout: a Payout's balance
--                               transactions of type 'payment' name it as their
--                               source. Recorded when the transfer is sent;
--                               NULL on rows that predate this column until the
--                               reconciler fills it from the Transfer.
--   stripe_bank_payout_id       the po_… that carried it to the bank.
--   bank_paid_at                when Stripe said that payout was paid
--                               (payout.paid on the connected-account endpoint).
--
-- "Earned all time" = sum of 'paid' rows (money that left the platform; a
-- failed transfer is never earned). "Paid out" = the subset with bank_paid_at;
-- "on its way" = the rest.

ALTER TABLE public.payouts
  ADD COLUMN IF NOT EXISTS stripe_destination_payment text,
  ADD COLUMN IF NOT EXISTS stripe_bank_payout_id text,
  ADD COLUMN IF NOT EXISTS bank_paid_at timestamp with time zone;

CREATE INDEX IF NOT EXISTS idx_payouts_destination_payment
  ON public.payouts USING btree (stripe_destination_payment)
  WHERE stripe_destination_payment IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_payouts_in_transit
  ON public.payouts USING btree (supporter_id)
  WHERE status = 'paid' AND bank_paid_at IS NULL;

COMMENT ON COLUMN public.payouts.stripe_destination_payment IS
  'The py_… the Transfer created on the connected account (Transfer.destination_payment). Join key to the connected-account Payout that carries it to the bank. Written at transfer time; backfilled from the Transfer by the reconciler for older rows.';
COMMENT ON COLUMN public.payouts.stripe_bank_payout_id IS
  'The connected-account Payout (po_…) that swept this transfer to the supporter''s bank. Set with bank_paid_at.';
COMMENT ON COLUMN public.payouts.bank_paid_at IS
  'When Stripe reported the connected-account payout carrying this transfer as paid (payout.paid). NULL = still on its way to the bank. Never set on a failed row.';
