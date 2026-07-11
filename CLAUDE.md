## Database Access Rules

Non-negotiable, for all client code (`app/src/` and future `mobile/`).
Authoritative version: `skills/constitution/STANDARDS.md` S-01, S-10, S-11.

### Rule 1 — All Hora table reads/writes go through the Go backend API

All reads and writes to Hora MVP tables (`users`, `profiles`, `tasks`,
`worklogs`, `notifications`, `task_gps_pings`, `reviews`) **MUST** go through
the Go backend API. Never use the Supabase JS client, PostgREST, or any
direct database connection from client code for these tables.

### Rule 2 — Permitted direct Supabase calls from clients

Exactly two:
- `supabase.auth.*` (signInWithOtp, verifyOtp, getSession, onAuthStateChange, signOut, etc.)
- Reading public avatar URLs from the `avatars` storage bucket

### Rule 3 — RLS is a lock, not an authorization layer

Every table has RLS **enabled with zero policies** (deny-all, verified
2026-07-11 — see `skills/decisions/D-02`). This blocks all anon/authenticated
PostgREST access; it authorizes nothing. The Go backend is the ONLY
authorization layer. Never add an RLS policy to "let the client read
something" — that opens a client-direct path and violates Rule 1.

### Rule 4 — Why

The Go backend connects directly as the `postgres` role (bypassing RLS) and
enforces all permissions in application code. Client-held anon keys hit the
deny-all wall; identity for business logic is the internal `users.id` UUID,
obtained via the `/auth/exchange` flow — never a raw Supabase token `sub`.