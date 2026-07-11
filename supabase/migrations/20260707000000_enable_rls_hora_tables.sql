-- Enable RLS on all Hora MVP tables.
-- No permissive policies are added, so anon and authenticated roles get deny-all.
-- The Go backend connects as the postgres superuser (via Supabase pooler) and
-- bypasses RLS automatically. FORCE ROW LEVEL SECURITY is intentionally omitted.

ALTER TABLE public.users             ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.profiles          ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.tasks             ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.worklogs          ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.notifications     ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.task_gps_pings    ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.reviews           ENABLE ROW LEVEL SECURITY;
