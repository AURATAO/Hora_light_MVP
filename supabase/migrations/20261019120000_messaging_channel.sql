-- Messaging entry point: requesters text HO:RA over iMessage (Sendblue) or
-- WhatsApp (Meta Cloud API). See PLAN.md §5 and server/internal/channel.
--
-- WHAT THIS ADDS (new tables only — nothing existing is altered)
--
--   public.channel_identities     a phone number on a channel, and the HO:RA
--                                 user it belongs to once set up. user_id is
--                                 NULL until the person completes the one-page
--                                 setup (name, email, terms, card). Linking an
--                                 existing account is proven by email OTP on
--                                 that page — never by matching profiles.phone,
--                                 which is free text nobody verified.
--   public.channel_messages       every inbound and outbound message. The
--                                 unique (channel, provider_message_id) is the
--                                 webhook dedupe key: both providers retry, and
--                                 a retried "YES" must not approve twice.
--                                 Outbound rows carry task_id so an inline reply
--                                 to them resolves to that task (router step 5).
--   public.channel_conversations  one row per identity: where the conversation
--                                 is (drafting, awaiting a YES, awaiting an
--                                 approval…), the pending draft, and when the
--                                 person last wrote (WhatsApp 24-hour window).
--   public.channel_setup_tokens   single-use setup links, stored hashed,
--                                 30-minute expiry.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): every table ships with RLS ENABLED,
-- ZERO policies, and an explicit REVOKE from PUBLIC/anon/authenticated (the
-- REVOKE is load-bearing against the schema's default privileges; see
-- 20260911120000). These tables hold phone numbers and message text (S-12):
-- Go is the only reader and writer.
--
-- Invariant check after applying — all four CLAUDE.md Rule 3 queries: 1 still
-- returns exactly the three deny-all policies and names none of these tables;
-- 2–4 return nothing.
--
-- Reversible:
--   DROP TABLE public.channel_setup_tokens;
--   DROP TABLE public.channel_conversations;
--   DROP TABLE public.channel_messages;
--   DROP TABLE public.channel_identities;
--
-- Deploy order (S-21): apply BEFORE the Go build that sets
-- CHANNEL_WEBHOOKS_ENABLED=1. Dry-run on a Supabase branch first. NOT applied
-- to production during the App Store review freeze (PLAN §2).

-- ── channel_identities ─────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.channel_identities (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  channel      text NOT NULL CHECK (channel IN ('imessage', 'whatsapp')),
  -- E.164, normalized by channel.NormalizeE164 before every insert/lookup.
  address      text NOT NULL CHECK (address ~ '^\+[1-9][0-9]{7,14}$'),
  user_id      uuid REFERENCES public.users(id),
  -- When the person proved this number is theirs by completing setup from a
  -- link sent to it. NULL = we have only seen messages from it.
  verified_at  timestamp with time zone,
  created_at   timestamp with time zone NOT NULL DEFAULT now(),

  CONSTRAINT channel_identities_verified_has_user CHECK (
    verified_at IS NULL OR user_id IS NOT NULL
  )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_channel_identities_address
  ON public.channel_identities USING btree (channel, address);
CREATE INDEX IF NOT EXISTS idx_channel_identities_user
  ON public.channel_identities USING btree (user_id) WHERE user_id IS NOT NULL;

ALTER TABLE public.channel_identities ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.channel_identities FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.channel_identities IS
  'Phone number on a messaging channel ↔ HO:RA user. Written by the Go channel router and setup page only. RLS deny-all, zero policies (S-10). Holds phone numbers (S-12).';

-- ── channel_messages ───────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.channel_messages (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  channel               text NOT NULL CHECK (channel IN ('imessage', 'whatsapp')),
  -- Sendblue message_handle / WhatsApp wamid.
  provider_message_id   text NOT NULL CHECK (provider_message_id <> ''),
  direction             text NOT NULL CHECK (direction IN ('inbound', 'outbound')),
  -- The person's number (sender of inbound, recipient of outbound), E.164.
  address               text NOT NULL,
  identity_id           uuid REFERENCES public.channel_identities(id),
  -- The task this message was attributed to by the router (inbound) or sent
  -- about (outbound). NULL for drafting, setup and small talk.
  task_id               uuid REFERENCES public.tasks(id),
  reply_to_provider_id  text,
  -- iMessage / SMS / RCS / whatsapp — what actually carried it.
  service               text,
  body                  text NOT NULL DEFAULT '',
  received_at           timestamp with time zone NOT NULL DEFAULT now(),
  -- Set when the router finished with an inbound message. NULL after a crash
  -- between recording and routing → picked up by the recovery sweep.
  processed_at          timestamp with time zone,
  created_at            timestamp with time zone NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_channel_messages_provider_id
  ON public.channel_messages USING btree (channel, provider_message_id);
CREATE INDEX IF NOT EXISTS idx_channel_messages_address_time
  ON public.channel_messages USING btree (channel, address, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_channel_messages_task
  ON public.channel_messages USING btree (task_id) WHERE task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_channel_messages_unprocessed
  ON public.channel_messages USING btree (received_at)
  WHERE direction = 'inbound' AND processed_at IS NULL;

ALTER TABLE public.channel_messages ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.channel_messages FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.channel_messages IS
  'Every message on the messaging channel; (channel, provider_message_id) is the webhook dedupe key. Go only. RLS deny-all, zero policies (S-10). Holds phone numbers and message text (S-12).';

-- ── channel_conversations ──────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.channel_conversations (
  identity_id            uuid PRIMARY KEY REFERENCES public.channel_identities(id),
  state                  text NOT NULL DEFAULT 'idle' CHECK (state IN (
                           'idle',
                           'drafting',
                           'awaiting_name',
                           'awaiting_confirm',
                           'awaiting_setup',
                           'awaiting_approval',
                           'awaiting_budget',
                           'awaiting_task_choice',
                           'handoff'
                         )),
  -- The task draft awaiting the person's YES: the parsed draft, the text it
  -- came from, and the quote shown (channel.PendingDraft JSON).
  pending_draft          jsonb,
  -- First name given in the chat before an account exists; written to the
  -- profile when the number is linked.
  pending_name           text,
  -- When the person replied YES to a summary that stated "by booking you
  -- agree to our Terms". Stamped onto profiles.terms_accepted_at at link.
  terms_accepted_at      timestamp with time zone,
  -- The budget/time request awaiting the person's YES/NO. Its own expiry
  -- (extension_requests) still decides the default outcome.
  pending_extension_id   uuid REFERENCES public.extension_requests(id),
  -- Candidate tasks offered in a "which task?" question, in the order listed.
  pending_task_choices   uuid[],
  -- The message held while asking which task it is for (M3.2), relayed on
  -- the pick. Message text, like pending_draft; purged with the identity.
  pending_relay_text     text,
  -- Last message FROM the person. WhatsApp free-form replies are allowed for
  -- 24h after this; later messages need an approved template.
  last_inbound_at        timestamp with time zone,
  updated_at             timestamp with time zone NOT NULL DEFAULT now()
);

ALTER TABLE public.channel_conversations ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.channel_conversations FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.channel_conversations IS
  'Per-identity conversation state for the messaging router (PLAN §6). Go only. RLS deny-all, zero policies (S-10).';

-- ── channel_setup_tokens ───────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.channel_setup_tokens (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  identity_id   uuid NOT NULL REFERENCES public.channel_identities(id),
  -- SHA-256 of the token in the link. The raw token is only ever in the
  -- message sent to the person.
  token_hash    text NOT NULL,
  expires_at    timestamp with time zone NOT NULL,
  used_at       timestamp with time zone,
  created_at    timestamp with time zone NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_channel_setup_tokens_hash
  ON public.channel_setup_tokens USING btree (token_hash);

ALTER TABLE public.channel_setup_tokens ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.channel_setup_tokens FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.channel_setup_tokens IS
  'Single-use, 30-minute setup links sent over the messaging channel; stored hashed. Go only. RLS deny-all, zero policies (S-10).';

NOTIFY pgrst, 'reload schema';

-- Columns added to channel_conversations while this migration was still
-- unshipped (M3.1–M3.5, M3.2). CREATE TABLE IF NOT EXISTS above is a no-op
-- where the table already exists (a staging branch, a local database), so
-- they are added here as well, idempotently.
ALTER TABLE public.channel_conversations
  ADD COLUMN IF NOT EXISTS pending_task_choices uuid[],
  ADD COLUMN IF NOT EXISTS pending_relay_text   text;
