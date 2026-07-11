-- Enable RLS on public.timesheets (orphaned table — zero rows, zero code references).
-- Schema resembles an early prototype of task_gps_pings that was abandoned.
-- Deny-all for anon/authenticated; postgres superuser bypasses automatically.

ALTER TABLE public.timesheets ENABLE ROW LEVEL SECURITY;
