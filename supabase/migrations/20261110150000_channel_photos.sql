-- Inbound photos on the messaging channel (customer → supporter). A requester
-- texts a photo over iMessage/SMS (Sendblue) or WhatsApp — a return label or
-- QR code, the item to buy, where to leave something. Until now the router
-- received it and dropped it. See server/internal/channel/photos.go.
--
-- WHAT THIS ADDS
--   storage bucket channel-photos   PRIVATE. 10 MB per object, images only
--                                   (JPEG, PNG, GIF, WebP, HEIC/HEIF). Written
--                                   and read by the Go backend with the
--                                   service key; clients get short-lived
--                                   signed links from handlers that have
--                                   already authorized them (the task's
--                                   requester or assignee, ops). No storage
--                                   policy is added: none is needed for the
--                                   service key, and any policy would open a
--                                   client path.
--   public.channel_photos           one row per stored photo:
--     identity_id        the number that sent it (NULL once that number is
--                        purged with its account — the row is then only a
--                        tombstone waiting for the sweeper)
--     channel, provider_message_id, position
--                        the inbound message it came with (Ops Chats shows
--                        it in the thread); unique — a webhook retry stores
--                        nothing twice
--     object_key         inbound/<identity id>/<random>.<ext> in the bucket.
--                        Never a provider URL: those expire and are bearer
--                        links; nothing stores them.
--     content_type, size_bytes
--                        judged from the bytes by the backend
--     task_id            the task it was booked with or relayed into (the
--                        task screen shows it to the supporter). SET NULL if
--                        an unpaid task row is discarded.
--     on_draft_at        put on a draft (no longer "loose")
--     relayed_at         passed into the task chat (TalkJS)
--     delete_requested_at
--                        account deletion: the sweeper deletes the object,
--                        then the row
--
-- RETENTION (server sweeper, channel_photos.go): a row with
-- delete_requested_at, or with no task 7 days after it arrived, has its
-- object and then its row deleted. A photo on a task stays with the task
-- until the requester's account is deleted.
--
-- Security (S-10 / CLAUDE.md Rule 3): RLS ENABLED, zero policies, explicit
-- REVOKE from PUBLIC/anon/authenticated. Go is the only reader and writer.
-- After applying, the four Rule 3 queries: 1 still returns exactly the three
-- deny-all policies; 2–4 return nothing. The storage schema's objects
-- policies are out of scope for those checks and are not changed here.
--
-- Deploy order: apply BEFORE the backend build that sets
-- CHANNEL_PHOTOS_ENABLED=1 (until then the backend keeps answering photos
-- with "I can only read text").
--
-- Reversible (only once no photos need keeping):
--   DROP TABLE public.channel_photos;
--   -- empty the bucket in the dashboard first, then:
--   DELETE FROM storage.buckets WHERE id = 'channel-photos';

CREATE TABLE IF NOT EXISTS public.channel_photos (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  identity_id          uuid REFERENCES public.channel_identities(id) ON DELETE SET NULL,
  channel              text NOT NULL CHECK (channel IN ('imessage', 'whatsapp')),
  provider_message_id  text NOT NULL CHECK (provider_message_id <> ''),
  position             smallint NOT NULL DEFAULT 0 CHECK (position >= 0),
  object_key           text NOT NULL CHECK (object_key LIKE 'inbound/%' AND object_key NOT LIKE '%..%'),
  content_type         text NOT NULL CHECK (content_type IN (
                         'image/jpeg', 'image/png', 'image/gif', 'image/webp', 'image/heic', 'image/heif')),
  size_bytes           integer NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 10485760),
  task_id              uuid REFERENCES public.tasks(id) ON DELETE SET NULL,
  on_draft_at          timestamp with time zone,
  relayed_at           timestamp with time zone,
  delete_requested_at  timestamp with time zone,
  created_at           timestamp with time zone NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_channel_photos_message
  ON public.channel_photos USING btree (channel, provider_message_id, position);
CREATE UNIQUE INDEX IF NOT EXISTS uq_channel_photos_object
  ON public.channel_photos USING btree (object_key);
CREATE INDEX IF NOT EXISTS idx_channel_photos_task
  ON public.channel_photos USING btree (task_id) WHERE task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_channel_photos_loose
  ON public.channel_photos USING btree (identity_id, created_at)
  WHERE task_id IS NULL AND on_draft_at IS NULL AND relayed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_channel_photos_sweep
  ON public.channel_photos USING btree (created_at)
  WHERE task_id IS NULL OR delete_requested_at IS NOT NULL;

ALTER TABLE public.channel_photos ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.channel_photos FROM PUBLIC, anon, authenticated;
COMMENT ON TABLE public.channel_photos IS
  'Photos people text to the messaging channel; objects in the private channel-photos bucket. Go only. RLS deny-all, zero policies (S-10). Personal data (S-12): deleted with the account.';

-- The bucket. Guarded so the migration also runs on a plain Postgres (the
-- test databases have no storage schema).
DO $$
BEGIN
  IF to_regclass('storage.buckets') IS NOT NULL THEN
    INSERT INTO storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
    VALUES ('channel-photos', 'channel-photos', false, 10485760,
            ARRAY['image/jpeg', 'image/png', 'image/gif', 'image/webp', 'image/heic', 'image/heif'])
    ON CONFLICT (id) DO UPDATE
      SET public = false,
          file_size_limit = EXCLUDED.file_size_limit,
          allowed_mime_types = EXCLUDED.allowed_mime_types;
  END IF;
END $$;

NOTIFY pgrst, 'reload schema';
