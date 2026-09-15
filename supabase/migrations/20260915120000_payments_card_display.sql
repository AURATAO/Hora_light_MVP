-- The card a hold was placed on, recorded at authorization time.
--
-- WHY STORE IT RATHER THAN LOOK IT UP. A requester needs to be told WHICH card
-- their money is reserved on ("Visa ••4242") — a silent off-session pre-auth
-- that names no card reads as nothing having happened, which is how a live
-- tester concluded their post had failed and cancelled it.
--
-- The alternative was reading the customer's current default card at render
-- time. That is wrong twice over: it is a Stripe API call on every poll of a
-- task screen, and it answers a different question. The hold sits on the card
-- it was authorized against; if the requester later changes their default, or
-- removes that card, the hold does not move. A ledger row records what
-- happened, not what is currently configured.
--
-- Display only, and deliberately the least that will do. Brand and last four
-- are what a cardholder uses to recognise their own card and are printed on
-- receipts everywhere; they are not a credential and cannot be replayed. No
-- fingerprint, no PaymentMethod id, nothing that would make a leaked row more
-- interesting than it already is — same reasoning as SavedCard in
-- payments_cards.go.
--
-- Nullable, and stays null on: every existing row, any task posted with
-- PAYMENTS_ENFORCED off, and any authorization where Stripe did not return the
-- charge detail. Every read path treats absence as "don't name a card" rather
-- than guessing, so a null here degrades the copy and nothing else.
--
-- Reversible:
--   ALTER TABLE public.payments DROP COLUMN card_brand, DROP COLUMN card_last4;
--
-- Deploy order (S-21): apply BEFORE the Go build that writes these.

ALTER TABLE public.payments
  ADD COLUMN IF NOT EXISTS card_brand text,
  ADD COLUMN IF NOT EXISTS card_last4 text
    CHECK (card_last4 IS NULL OR card_last4 ~ '^[0-9]{4}$');

COMMENT ON COLUMN public.payments.card_brand IS
  'Display only: the card brand this hold was authorized against ("visa"), as Stripe reported it at authorization. Not the requester''s current default card — the hold does not move when that changes.';
COMMENT ON COLUMN public.payments.card_last4 IS
  'Display only: last four digits of the card this hold was authorized against. Shown to the REQUESTER only, never to the supporter.';
