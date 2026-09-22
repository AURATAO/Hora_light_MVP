-- Build 12, piece 12: display names everywhere.
--
-- Until now every profile was created with `name` seeded from the email's
-- local part ("taoaura.lavoro@…" → "Taoaura Lavoro"), and every surface that
-- checked "does this person have a name?" saw one. The seed is not a name; it
-- is what the server printed when nobody had asked. From this build the Go
-- side never seeds it, resolves the email prefix at read time only as a last
-- resort (helpers.DisplayName), and prompts once — "What should we call you?"
-- — for an account whose profiles.name is empty.
--
-- This blanks the seeds so that prompt fires. A name is treated as seeded
-- when, ignoring case, dots and spaces, it is the email's local part — which
-- catches the Go seed (title-cased, dots → spaces), the handle_new_user
-- trigger's raw split_part, and anything a client once echoed back. A person
-- who deliberately chose a name identical to their address will be asked
-- once and can type it again; that is the cheaper mistake.
update public.profiles
   set name = '',
       updated_at = now()
 where coalesce(email, '') <> ''
   and coalesce(name, '') <> ''
   and lower(regexp_replace(name, '[\s.]+', '', 'g'))
       = lower(regexp_replace(split_part(email, '@', 1), '[\s.]+', '', 'g'));
