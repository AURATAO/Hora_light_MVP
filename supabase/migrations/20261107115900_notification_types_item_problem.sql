-- Item problem on shopping tasks (Aura 2026-11-07; server/item.go). Its own
-- file because a new enum value cannot be USED in the transaction that adds
-- it (see 20260923104029): applied before 20261107120000_item_problem.sql and
-- before any build that reports an item.
--
--   ITEM_ALTERNATIVE_PROPOSED  to the requester: an item is out of stock and
--                              the supporter found an alternative inside the
--                              budget — the 1/2 question (by text) or the
--                              two buttons (web).
--   ITEM_UNAVAILABLE           to the requester: out of stock, nothing
--                              similar; the supporter will skip it. A notice.
--
-- Reversible: enum values cannot be dropped; left unused they are inert.

ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'ITEM_ALTERNATIVE_PROPOSED';
ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'ITEM_UNAVAILABLE';
