# Hora MVP — Backend Architecture Reference

> Generated 2026-07-07. Intended as context for AI-assisted development on a new app sharing this Supabase project / Go backend.

---

## 0. Two Supabase Projects — Critical Orientation

This repo connects to **two completely separate Supabase projects**:

| | Project | Ref | Used by |
|---|---|---|---|
| **Hora MVP** | (unnamed) | `akxsdkerudurzcemurrb` | Go backend (direct Postgres) |
| **AURATAO's Project** | birthday-card app | `aemwljralqsegrwivbub` | Separate app (events, cards, people tables) |

The Go backend **does not use PostgREST or the Supabase JS client** at all. It connects directly to the Hora MVP Postgres via pgx using `SUPABASE_DB_URL`. Everything documented in sections 1–3 refers to the `akxsdkerudurzcemurrb` project unless stated otherwise.

---

## 1. Supabase Schema (Hora MVP — `akxsdkerudurzcemurrb`)

Schema is inferred from Go SQL queries (no migration files exist in this repo).

### 1.1 Tables

#### `public.users`
The canonical identity table. Created by the Go backend, not by Supabase auth.

| Column | Type | Nullable | Default | Notes |
|---|---|---|---|---|
| `id` | uuid | NO | gen_random_uuid() | Internal primary key |
| `supabase_sub` | uuid | YES | — | Supabase auth user UUID (from magic-link / OTP) |
| `google_sub` | text | YES | — | Google OIDC subject |
| `email` | text | YES | — | Unique. Used for account linking across auth providers |
| `name` | text | YES | — | Display name |
| `picture` | text | YES | — | Avatar URL from Google OAuth |

- No FK to `auth.users`. The Go backend manages this table independently.
- Account linking: when a user signs in via a new provider, the backend matches by email and sets the new `_sub` column on the existing row.

#### `public.profiles`
User-facing profile data. `id` is kept in sync with `users.id` (same UUID).

| Column | Type | Nullable | Default | Notes |
|---|---|---|---|---|
| `id` | uuid | NO | — | PK, kept = users.id |
| `email` | text | YES | — | Unique, used as lookup key |
| `name` | text | YES | — | |
| `phone` | text | YES | — | |
| `city` | text | YES | — | |
| `avatar_url` | text | YES | — | Public URL in `avatars` storage bucket |
| `bio` | text | YES | — | |
| `beta_accepted` | boolean | YES | false | |
| `is_verified_supporter` | boolean | NO | false | Set by `POST /ops/supporter-approve` (was: manual admin edit) |
| `supporter_applied_at` | timestamptz | YES | — | Set by `POST /supporter/apply` |
| `supporter_rejected_at` | timestamptz | YES | — | Set by `POST /ops/supporter-reject`; cleared by `POST /supporter/apply` and `/ops/supporter-approve` (D-08) |
| `created_at` | timestamptz | NO | now() | |
| `updated_at` | timestamptz | NO | now() | |

- `supporter_status` is a derived field computed in Go (`approved` / `rejected` / `applied` / `none`), not a DB column.

#### `public.tasks`
Core task marketplace table.

| Column | Type | Nullable | Default | Notes |
|---|---|---|---|---|
| `id` | uuid | NO | gen_random_uuid() | |
| `title` | text | NO | — | |
| `description` | text | YES | — | |
| `category` | text | NO | — | Enum: `task`, `companion`, `quick_errand`, `standard`, `half_day`, `full_day`, `delivery`, `grocery`, `laundry`, `queue`, `anything_else`, `companionship` |
| `location_text` | text | YES | — | Free-text address |
| `estimated_minutes` | int | YES | 30 | |
| `prepay_amount_cents` | int | YES | 0 | Pre-authorized amount in cents |
| `is_immediate` | boolean | YES | — | true = ASAP |
| `scheduled_at` | timestamptz | YES | — | Null if immediate |
| `requester` | text | YES | — | **Requester email** (legacy; UUID is the source of truth) |
| `requester_id` | uuid | YES | — | FK → `users.id` |
| `status` | text | NO | `'open'` | Values: `open`, `completed`, `cancelled` |
| `assigned_to` | text | YES | — | **Assignee email** (legacy) |
| `assigned_to_id` | uuid | YES | — | FK → `users.id`; null until accepted |
| `transport_required` | text | YES | `'none'` | |
| `travel_time_minutes` | int | YES | — | Set by `/estimate-travel` |
| `total_estimate_minutes` | int | YES | — | `estimated_minutes + travel_time_minutes` |
| `completion_photo_url` | text | YES | — | URL in `task-completions` bucket |
| `completion_note` | text | YES | — | |
| `completed_at` | timestamptz | YES | — | |
| `cancel_reason` | text | YES | — | |
| `cancelled_at` | timestamptz | YES | — | |
| `created_via` | text | YES | — | Attribution: `form`, `ai_parse`, `duplicate`. Write-once by `POST /tasks`; `PATCH` never touches it. NULL = unattributed (rows predating the column, and web, which doesn't send it) |
| `created_at` | timestamptz | NO | now() | |

- Both email and UUID columns are stored for requester/assignee. **Always use UUID columns for permission checks**; email columns are used by worklogs (legacy) and some notification lookups.

#### `public.worklogs`
Time-tracking sessions per task (a supporter can clock in/out multiple times).

| Column | Type | Nullable | Default | Notes |
|---|---|---|---|---|
| `id` | uuid | NO | gen_random_uuid() | |
| `task_id` | uuid | NO | — | FK → `tasks.id` |
| `user` | text | NO | — | Assignee email (legacy; not UUID) |
| `start_at` | timestamptz | NO | now() | |
| `end_at` | timestamptz | YES | — | Null = currently clocked in |
| `created_at` | timestamptz | NO | — | |
| `updated_at` | timestamptz | NO | — | |

- Duration billing: each session is `ceil(seconds/60)` minutes, minimum 1 minute.

#### `public.notifications`
In-app + email notification log.

| Column | Type | Nullable | Default | Notes |
|---|---|---|---|---|
| `id` | uuid | NO | gen_random_uuid() | |
| `user_id` | uuid | NO | — | FK → `users.id` (recipient) |
| `task_id` | uuid | YES | — | FK → `tasks.id` |
| `type` | text | NO | — | `ORDER_ACCEPTED`, `CLOCK_IN`, `CLOCK_OUT`, `COMPLETED`, `COMPLETED_SUPPORTER`, `CANCELLED`, `NEW_MESSAGE` |
| `title` | text | NO | — | |
| `body` | text | NO | — | |
| `unread` | boolean | NO | true | |
| `via_email` | boolean | NO | — | Whether an email was sent |
| `email_sent_at` | timestamptz | YES | — | |
| `created_at` | timestamptz | NO | now() | |

#### `public.task_gps_pings`
Live location pings from a supporter during an active work session.

| Column | Type | Nullable | Default | Notes |
|---|---|---|---|---|
| `id` | uuid | NO | gen_random_uuid() | |
| `task_id` | uuid | NO | — | FK → `tasks.id` |
| `user_id` | uuid | NO | — | FK → `users.id` |
| `lat` | float8 | NO | — | |
| `lng` | float8 | NO | — | |
| `accuracy` | int | YES | — | Metres |
| `created_at` | timestamptz | NO | now() | |

#### `public.reviews`
Post-completion reviews. One review per task, written by the requester about the supporter.

| Column | Type | Nullable | Default | Notes |
|---|---|---|---|---|
| `id` | uuid | NO | gen_random_uuid() | |
| `task_id` | uuid | NO | — | FK → `tasks.id`; unique constraint implied |
| `reviewer_id` | uuid | NO | — | FK → `users.id` (requester) |
| `supporter_id` | uuid | NO | — | FK → `users.id` (assignee) |
| `stars` | int | NO | — | 1–5 |
| `value_rating` | text | YES | — | `not_worth`, `fair`, `great` |
| `would_rehire` | boolean | YES | — | |
| `comment` | text | YES | — | |
| `created_at` | timestamptz | NO | now() | |

### 1.2 Views

#### `public.view_ops_tasks`
Admin-only denormalized view used by the `/ops/feed` endpoint.

Columns exposed: `task_id`, `title`, `category`, `location_text`, `status`, `estimated_minutes`, `prepay_amount`, `is_immediate`, `scheduled_at`, `created_at`, `cancelled_at`, `cancel_reason`, `requester_email`, `supporter_email`, `first_start_at`, `last_end_at`, `total_minutes_done`, `running_minutes`, `last_event_at`.

- Joins `tasks` with `worklogs` to aggregate time data.
- `running_minutes` = minutes in currently-open sessions.
- `total_minutes_done` = sum of completed sessions.

### 1.3 RLS Policies and client grants

*Corrected 2026-09-10. This section previously read "None exist on Hora MVP tables", which was wrong — see the note at the end.*

Current state, and the state to keep:

| | |
|---|---|
| RLS | **Enabled on all 11 tables** in `public`. `relforcerowsecurity` is **false** everywhere, which is what lets the Go backend (owner role `postgres`) bypass RLS. |
| Policies | **Exactly three**, all `deny all (client)` — on `users`, `notifications`, `audit_logs`, `TO anon, authenticated USING (false) WITH CHECK (false)`. |
| Grants to `anon` / `authenticated` / `PUBLIC` | **None** on any Hora table. |
| SECURITY DEFINER functions | **None** in `public`. |

The eight tables without an explicit policy (`tasks`, `worklogs`, `profiles`, `messages`, `reviews`, `task_gps_pings`, `device_push_tokens`, `timesheets`) are deny-all by having RLS on with zero policies. The three that carry one have it as a second lock.

Authorization is enforced entirely at the Go application layer. The direct Postgres connection uses the `postgres` role (via pooler), so Supabase RLS is bypassed for Go and only Go.

**The `storage` schema is separate and intentionally not locked down this way:** `storage.objects` has six policies serving the public `avatars` bucket, which is one of the two client-direct calls CLAUDE.md Rule 2 permits.

#### What this section used to say, and why it was wrong

Until 2026-09-10 this read "**None exist on Hora MVP tables**", and CLAUDE.md Rule 3 said "zero policies, verified 2026-07-11" citing D-02.

In fact `public` carried **twelve** policies, nine of them PERMISSIVE grants to `authenticated`, and all twelve were present in the 2026-07-11 baseline migration itself (`20260711094158_remote_schema.sql` lines 783–858) — the same migration D-02 cited. The nine allowed an authenticated user, with the publishable key from any browser, to update their own `profiles` row (including `is_verified_supporter`), update their own `tasks` row (including `status` and `prepay_amount_cents`), and insert/update their own `worklogs`.

It went unnoticed because the exposure test used the **anon** key while the policies were scoped to **authenticated** — so `curl /rest/v1/tasks` returned `[]`, indistinguishable from deny-all — and because `pg_tables.rowsecurity` was checked while `pg_policies` was deferred as a "residual, non-blocking" follow-up that never happened. D-02 itself hedged with "(presumed) zero policies"; the hedge was dropped when the claim was copied here and into S-10.

Closed by `20260910150000_close_client_direct_table_access.sql` (drop the nine, revoke grants on the four tables involved, drop `current_user_id()`) and `20260910160000_revoke_residual_client_grants.sql` (revoke the remaining inert grants so the invariant check returns zero rows).

**Do not re-verify this section by inference or by an anon-key curl.** Run the four queries in CLAUDE.md Rule 3.

### 1.4 Postgres Functions

Called by the `/ops/*` admin endpoints:

| Function | Signature | Purpose |
|---|---|---|
| `force_complete` | `(task_id uuid)` | Admin-force a task to `completed` status |
| `cancel_task` | `(task_id uuid, reason text)` | Admin cancel with reason |
| `adjust_time` | `(task_id uuid, delta int)` | Add/subtract minutes from logged time |

### 1.5 Postgres Triggers

None detected in code.

### 1.6 Storage Buckets

| Bucket | Used by | Upload path pattern |
|---|---|---|
| `avatars` | `POST /profile/avatar` | `{uid}/avatar-{timestamp}.{ext}` |
| `task-completions` | `POST /tasks/:id/completion-photo` | `completions/{task_id}/{timestamp}.{ext}` |

- `avatars` upload uses signed URL flow (public read).
- `task-completions` upload uses service-role key directly (POST, no signed URL).

### 1.7 Edge Functions

| Function | File | Trigger | Purpose |
|---|---|---|---|
| `notify-new-task` | `supabase/functions/notify-new-task/index.ts` | HTTP POST | Sends admin email via Postmark when a new task is posted |

- This is an **early/duplicate** implementation. The Go backend's `notify.NotifyAdminNewTask()` now handles the same job. Both send to the same admin addresses. The Edge Function may have been wired to a DB webhook trigger that has since been removed or may still be active.

---

## 2. Go Backend API

- **Framework**: Gin
- **Port**: 8080 (env `PORT`)
- **Database**: pgxpool (native) + database/sql (stdlib wrapper for some queries)
- **Base URL (prod)**: set via `APP_BASE_URL` env

### 2.1 Auth Middleware

Two middleware variants are used:

| Middleware | Behavior |
|---|---|
| `dualAuth(db)` | Required auth. Checks cookie first, then Bearer token. 401 if neither passes. |
| `tryAuth(db)` | Optional auth. Sets uid/email if token is valid, continues regardless. |

**Cookie path (Google OAuth or magic-link exchange):**
- Cookie name: `hora_session`
- Algorithm: HS256, secret: `SESSION_JWT_SECRET` env
- Claims: `sub` = internal `users.id` UUID, `email`
- TTL: 24h

**Bearer path (Supabase access token):**
- Algorithm: RS256, validated against `SUPABASE_JWKS_URL`
- Also accepts HS256 tokens validated against `SUPABASE_JWT_SECRET`
- `sub` in Supabase token = Supabase UUID → mapped to internal UUID via `users.supabase_sub`

### 2.2 Endpoint Reference

All authenticated endpoints require either `hora_session` cookie or `Authorization: Bearer <supabase-token>`.

#### Auth

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/auth/login` | None | Redirects to Google OAuth |
| GET | `/auth/callback` | None | Google OAuth callback; sets `hora_session` cookie |
| POST | `/auth/logout` | None | Clears `hora_session` cookie |
| POST | `/auth/exchange` | None | Swap Supabase access token for `hora_session` cookie |
| GET | `/auth/me` | Optional | Returns `{auth, id, email, name, is_verified_supporter}` or `{auth: false}` |

**`POST /auth/exchange` body:** `{ "access_token": "<supabase_jwt>" }`
**`POST /auth/exchange` response:** `{ "auth": true, "id": "<internal_uuid>", "email": "", "name": "" }` + sets cookie

#### Profile

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/profile` | Required | Get own profile (lazy-creates if missing) |
| PATCH | `/profile` | Required | Update own profile fields |
| POST | `/profile/avatar` | Required | Upload avatar (multipart/form-data; field `file`; max 5MB) |
| GET | `/profiles/:id` | Optional | Get public profile by internal UUID |
| GET | `/profiles/:id/tasks` | Optional | List profile's tasks (`?role=requester\|assignee&status=open\|completed\|all&limit=&before=`) |
| GET | `/profiles/:id/reviews` | Optional | List reviews received as supporter |

**`PATCH /profile` body:** `{ "name"?, "phone"?, "city"?, "avatar_url"?, "bio"?, "beta_accepted"? }` (all optional)

**`GET /profile` response:**
```json
{
  "email", "name", "phone", "city", "avatar_url", "bio",
  "beta_accepted", "is_verified_supporter", "supporter_applied_at",
  "supporter_rejected_at",
  "supporter_status": "none|applied|approved|rejected",
  "created_at", "updated_at"
}
```

**`GET /profiles/:id` response:**
```json
{
  "id", "name", "city", "phone", "avatar_url", "bio", "created_at",
  "posted_total", "posted_completed", "asg_in_progress", "asg_completed"
}
```

**`POST /profile/avatar` response (200):** `{ "url": "<public avatar URL>" }` (`server/main.go` closure registered via `addAvatarUploadRouteV1`, ~line 826)

#### Supporter

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/supporter/apply` | Required | Submit supporter application; sets `supporter_applied_at`, clears `supporter_rejected_at` (re-apply after rejection); emails admins |

**Body:** `{ "first_name"?, "last_name"? }`

**Response (200):** `{ "ok": true }` — does not return the updated profile; call `GET /profile` afterward if you need the refreshed `supporter_status`. (`applySupporterHandler`, `server/main.go:1014`)

#### Tasks

All task endpoints require auth. Pagination uses keyset cursors: `?before_created_at=<RFC3339>&before_id=<uuid>`.

| Method | Path | Who | Purpose |
|---|---|---|---|
| POST | `/tasks` | Any | Create a task |
| GET | `/tasks` | Any | List own posted tasks (= `/tasks/posted`) |
| GET | `/tasks/posted` | Any | List own open posted tasks |
| GET | `/tasks/posted/closed` | Any | List own completed/cancelled posted tasks |
| GET | `/tasks/available` | Verified supporter only | List open unassigned tasks (not own) |
| GET | `/tasks/assigned` | Any | List tasks assigned to me with status=open |
| GET | `/tasks/done` | Any | List tasks assigned to me with status=completed |
| GET | `/tasks/:id` | Requester or assignee | Get task detail |
| PATCH | `/tasks/:id` | Requester only | Update open task fields |
| POST | `/tasks/:id/accept` | Verified supporter, not requester | Accept an open unassigned task |
| POST | `/tasks/:id/complete` | Requester or assignee | Mark task complete (requires ≥1 closed worklog + completion photo) |
| POST | `/tasks/:id/cancel` | Requester only | Cancel open unassigned task |
| POST | `/tasks/:id/completion-photo` | Requester or assignee | Upload completion photo; returns `{ url }` |
| POST | `/tasks/:id/clock-in` | Assignee only | Start a work session |
| POST | `/tasks/:id/clock-out` | Assignee only | End the active work session |
| GET | `/tasks/:id/worklogs` | Requester or assignee | Get worklogs + `total_minutes` + `total_cost_cents` |
| POST | `/tasks/:id/gps-ping` | Assignee (while clocked in) | Save GPS location |
| GET | `/tasks/:id/gps-latest` | Requester or assignee | Get last GPS ping |
| POST | `/tasks/:id/estimate-travel` | Any (auth) | Compute travel time from supporter to task location |
| POST | `/tasks/:id/review` | Requester only (after completion) | Submit star rating |

**`POST /tasks` body:**
```json
{
  "title": "string",
  "description": "string",
  "category": "quick_errand|delivery|grocery|...",
  "location_text": "string",
  "estimated_minutes": 30,
  "prepay_amount_cents": 0,
  "is_immediate": true,
  "scheduled_at": "RFC3339 or empty",
  "transport_required": "none|...",
  "created_via": "form|ai_parse|duplicate (optional)"
}
```
`created_via` is optional attribution, stored once and never rewritten. Omit it
and the task is stored unattributed (NULL); send anything outside the three
values and the request is rejected with `400 invalid created_via`. Only mobile
sends it today. `PATCH /tasks/:id` takes the same body but ignores this field.

**`POST /tasks/:id/complete` body:** `{ "completion_photo_url": "string (required)", "completion_note": "string" }`

**`POST /tasks/:id/cancel` body:** `{ "reason": "string (required)" }`

**`POST /tasks/:id/estimate-travel` body:** `{ "supporter_lat": 40.7, "supporter_lng": -74.0 }`
Response: `{ "travel_minutes", "task_minutes", "total_minutes", "display" }`

**`POST /tasks/:id/review` body:** `{ "stars": 1-5, "value_rating": "not_worth|fair|great", "would_rehire": bool, "comment": "string" }`

**Pricing:** see [The Billing Model](#the-billing-model) below. Every constant
lives in `BillingConfig` (`server/billing.go`); `POST /tasks/estimate` returns
the itemized breakdown clients render verbatim.

**`POST /tasks/estimate` body:** `{ "category": "standard", "estimated_minutes": 60, "prepay_amount_cents": 1000 }`
Response:
```json
{
  "base_fee_cents": 1200,
  "included_minutes": 15,
  "per_minute_rate_cents": 50,
  "total_minutes": 60,
  "billable_minutes": 45,
  "time_cost_cents": 2250,
  "shopping_budget_cents": 1000,
  "total_cents": 4450,
  "shopping_cents": 1000
}
```
`shopping_cents` is deprecated — it duplicates `shopping_budget_cents` and is
sent only so that mobile builds shipped before Stripe Phase 1 keep rendering.
A budget above the $30.00 cap is rejected `400 shopping_budget_over_cap`.

#### Notifications

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/notifications` | Optional | List notifications (`?unread=true&limit=50&before=<RFC3339>`) |
| PATCH | `/notifications/:id/read` | Required | Mark one notification read |
| POST | `/notifications/mark-read-all` | Required | Mark all notifications read |
| DELETE | `/notifications/:id` | Required | Delete one notification |
| DELETE | `/notifications?read=true` | Required | Delete all read notifications |

#### Ops (Admin Only)

Access is restricted to a hardcoded email allowlist: `auratao.model@gmail.com`, `liang.you@horaapp.co`, `liang.you@arcodiax.com`, `rollod4@gmail.com`, `daniele@arcodiax.com`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/ops/ping` | Health check |
| GET | `/ops/feed` | Task feed (`?status=all|open|accepted|in_progress|completed|cancelled&q=<search>`) |
| POST | `/ops/force-complete` | Force-complete a task — body: `{ "task_id": "uuid" }` |
| POST | `/ops/cancel` | Admin cancel — body: `{ "task_id": "uuid", "reason": "string" }` |
| POST | `/ops/adjust-time` | Adjust logged time — body: `{ "task_id": "uuid", "delta": <int minutes> }` |
| GET | `/ops/supporter-applications` | Every profile with `supporter_applied_at IS NOT NULL` (pending + decided), newest first, max 500 — each row: `id, email, name, phone, city, supporter_applied_at, supporter_rejected_at, is_verified_supporter, supporter_status` |
| POST | `/ops/supporter-approve` | Approve a supporter application — body: `{ "profile_id": "uuid" }` or `{ "email": "..." }`; sets `is_verified_supporter = true`, clears `supporter_rejected_at` |
| POST | `/ops/supporter-reject` | Reject a supporter application — body: `{ "profile_id": "uuid" }` or `{ "email": "..." }`; sets `supporter_rejected_at = now()`, `is_verified_supporter = false`. 404 if no row matched |

#### Webhooks

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/webhooks/whatsapp` | WHATSAPP_VERIFY_TOKEN | Meta webhook verification handshake |
| POST | `/webhooks/whatsapp` | None | Receive WhatsApp Cloud API events (currently logs only, no task creation) |

#### AI

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/ai/parse-task` | Required | Parse free-text input into structured task fields via Claude API |

**Body:** `{ "input": "free text" }`
**Response:** `{ "title", "category", "description", "location_1", "location_2", "duration_minutes", "scheduled", "scheduled_time" }`
Uses `claude-sonnet-4-20250514` model.

#### Misc

| Method | Path | Purpose |
|---|---|---|
| GET | `/` | Redirect to `APP_BASE_URL` |
| GET | `/__routes` | Debug: list all registered routes |

### 2.3 Notification System

Every state-change action triggers two side effects (goroutine/async):

1. **In-app notification** — writes to `public.notifications`
2. **Email** — sends via Postmark (`POSTMARK_API_TOKEN`) to the recipient's email

Events and recipients:
- Task accepted → requester gets `ORDER_ACCEPTED`
- Clock-in → requester gets `CLOCK_IN`
- Clock-out → requester gets `CLOCK_OUT`
- Task completed → requester gets `COMPLETED`, assignee gets `COMPLETED_SUPPORTER`
- Task cancelled → requester gets `CANCELLED`, assignee (if any) gets `CANCELLED`
- New task posted → admin emails sent directly (not stored as notification)
- Supporter applied → admin emails sent directly

WhatsApp notifications are stubbed (TODO comments throughout).

### 2.4 External Dependencies

| Service | Env Var(s) | Usage |
|---|---|---|
| Supabase Postgres | `SUPABASE_DB_URL` | Direct connection (pgxpool) |
| Supabase Storage | `SUPABASE_PROJECT_URL`, `SUPABASE_SERVICE_ROLE_KEY` | Avatar + photo uploads |
| Supabase Auth JWKS | `SUPABASE_JWKS_URL` | Validate Bearer tokens |
| Google OAuth | `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | Login flow |
| Google Maps | `GOOGLE_MAPS_API_KEY` | Distance Matrix API for travel estimates |
| Postmark | `POSTMARK_API_TOKEN` | Transactional email |
| Anthropic | `ANTHROPIC_API_KEY` | AI task parsing |
| WhatsApp (Meta) | `WHATSAPP_ACCESS_TOKEN`, `WHATSAPP_PHONE_NUMBER_ID`, `WHATSAPP_VERIFY_TOKEN` | Future notifications |

---

## 3. Data Flow Overview

### Frontend → Go Backend → Supabase

**All task lifecycle operations** go through the Go backend:
- Creating, reading, updating, accepting, cancelling, completing tasks
- Clock-in / clock-out / worklogs
- GPS pings
- Reviews
- Notifications (read/delete)
- Profile read/write
- Supporter application
- File uploads (avatar, completion photo) — Go acts as a proxy that gets a signed URL and forwards to Supabase Storage

**Frontend → Supabase directly (bypassing Go):**
- Supabase Auth (magic link OTP, session management) — frontend uses `@supabase/supabase-js`
- After Supabase auth completes, frontend calls `POST /auth/exchange` to obtain the `hora_session` cookie for Go backend calls
- Real-time subscriptions (if any) — would go direct to Supabase, but none are wired in the current frontend

**Auth handshake sequence (magic link / OTP):**
1. User enters email on frontend
2. Frontend calls Supabase `signInWithOtp` → Supabase sends magic link
3. User clicks link → Supabase sets `supabase.auth.session` in browser
4. Frontend calls `POST /auth/exchange` with the Supabase `access_token`
5. Go backend validates token, upserts `public.users`, returns `hora_session` cookie
6. All subsequent API calls use the cookie

**Auth handshake sequence (Google OAuth):**
1. Frontend redirects to `GET /auth/login` on Go backend
2. Go redirects to Google
3. Google redirects to `GET /auth/callback`
4. Go upserts `public.users` with `google_sub`, sets `hora_session` cookie
5. Go redirects to frontend

---

## 4. Key Notes for Building a New App on This Infrastructure

### What you can reuse directly

- **`public.users` table** — the auth backbone. Your new app can share this table. Match users by `email` (most reliable) or `supabase_sub` (for Supabase Auth users).
- **`public.profiles` table** — safe to read `name`, `avatar_url`, `city`, `is_verified_supporter` for any shared UI. Be careful with writes since the Go backend also writes here.
- **`public.tasks`, `public.worklogs`, `public.reviews`, `public.notifications`** — safe to read for analytics or cross-app features. Write access should go through the Go API to preserve business logic.
- **Supabase Auth** — the auth system (`akxsdkerudurzcemurrb`) is shared. Your new app can accept the same `hora_session` cookie if it uses the same `SESSION_JWT_SECRET`, or operate with Supabase access tokens and call `/auth/exchange`.
- **Supabase Storage** — `avatars` bucket is public-read; you can read avatar URLs directly.
- **`GET /auth/me`** — lightweight endpoint to check session validity and get `is_verified_supporter` flag.

### Gotchas and design notes

1. **Two UUID spaces.** `auth.users.id` (Supabase) ≠ `public.users.id` (internal). If you use Supabase Auth and `auth.uid()` in RLS policies, that UUID will NOT match the `id` values stored in `tasks.requester_id` etc. Always translate via `supabase_sub` or `google_sub` → `public.users.id`.

2. **Two separate Supabase projects.** The birthday-card app (`aemwljralqsegrwivbub`) and Hora MVP (`akxsdkerudurzcemurrb`) are distinct projects with separate auth, storage, and databases. If your new app is on `aemwljralqsegrwivbub`, you cannot directly query Hora MVP tables — you'd need a new connection.

3. **No RLS on Hora MVP tables.** The Go backend uses the Postgres `postgres` role and bypasses RLS. If you add a Supabase client (PostgREST/JS SDK) that uses the `anon` or `authenticated` roles and write RLS policies, be aware the Go backend will ignore them. Coordinate or use a service role client.

4. **`profiles.id` drift.** The Go backend has patching logic to keep `profiles.id == users.id`. If you insert profiles from a new app without this logic, IDs can diverge and permission checks will silently fail.

5. **Redundant email fields on tasks.** `tasks.requester` and `tasks.assigned_to` are email strings (legacy). The authoritative identity fields are `requester_id` and `assigned_to_id` (UUIDs). Don't rely on the email columns for new code.

6. **Worklogs use email, not UUID.** `worklogs.user` is the assignee's email, not their UUID. This is a known inconsistency. Cross-join with `users.email` to get the UUID.

7. **Pricing is computed in Go.** There is no `price` or `cost` column in `tasks`. Cost is always derived at query time by the Go backend from worklog duration. Do NOT reproduce the formula in a client — call `POST /tasks/estimate` (pre-submission) or read the `cost` object from `GET /tasks/:id/worklogs` (after work is logged). The web app carried three hand-copied duplicates of the schedule for two months; `app/src/lib/pricing.test.mjs` now fails the build if one comes back. See [The Billing Model](#the-billing-model).

8. **`supporter_status` is derived.** There's no `supporter_status` column in the DB. It's computed: if `is_verified_supporter = true` → `"approved"`; else if `supporter_rejected_at IS NOT NULL` → `"rejected"`; else if `supporter_applied_at IS NOT NULL` → `"applied"`; else `"none"`. Rejection outranks the application timestamp because `supporter_applied_at` is never cleared (D-08).

9. **Admin allowlist is hardcoded in Go.** The ops admin emails are a static map in `main.go`. Adding a new admin requires a code change and redeployment.

10. **WhatsApp is not functional.** The webhook receiver is wired and logs incoming messages, but no outbound notifications are sent (all are TODO). `helpers.SendWhatsAppMessage` works if env vars are set, but nothing calls it in production paths yet.

11. **Edge Function `notify-new-task` may be a zombie.** The Go backend's `NotifyAdminNewTask()` already handles admin emails for new tasks. The Edge Function does the same thing. Check whether a DB webhook trigger is still pointing at it in the Supabase dashboard; if so, admins will receive duplicate emails.

12. **Session cookie vs. Bearer — CORS.** The `hora_session` cookie uses `SameSite=None; Secure` in prod (cross-domain). If your new app is on a different origin, you need `credentials: 'include'` on fetch calls and the origin must be in `CORS_ALLOW_ORIGINS` or `*.vercel.app`.

---

## The Billing Model

The complete pricing, settlement and payment model. Phase 1 (this section's
subject) ships the billing engine, the payments schema and the Stripe webhook;
Phases 2 and 3 wire the money flows onto them. Everything described here as
"Phase 2/3" has schema and config support today and no code path that reaches
it yet.

**Single source of truth:** `BillingConfig` in `server/billing.go`. Every
constant below is a field of it. A billing number that is not a field of
`BillingConfig` is a bug — that is the rule that replaced two scattered
literals in `main.go` and three hand-copied duplicates in the web app.

### 1. Time billing

| | |
|---|---|
| Base fee | **$12.00** default, **$25.00** companionship/companion |
| Included | the **first 15 minutes** are inside the base fee |
| Billable minutes | `max(total_logged_minutes − 15, 0)` |
| Rate | **$0.50/min** on billable minutes only |
| Task time cost | `base_fee + billable_minutes × $0.50` |

`total_logged_minutes` is the sum across **all closed worklog sessions** on the
task. A laundry-style task clocked in and out three times bills the sum of the
three, and **the 15-minute inclusion is applied once against that sum, not once
per session.** Gaps between sessions are not billed and do not consume the
inclusion.

Session rounding is unchanged and happens *before* the sum: each session is
rounded up to a whole minute with a one-minute floor (`ceil`, `greatest(m,1)`).
Open sessions are excluded — a supporter still on the clock has logged nothing
billable yet.

The **$18.00 tier for `estimated_minutes > 90` was removed.** It was a duration
surcharge wearing a category's clothes: it double-charged for length that the
per-minute rate already bills, and it keyed off the requester's *estimate*
rather than time actually worked, so a task that overran its estimate was
cheaper than one estimated honestly.

**Cancellation.** A task cancelled before anyone clocked in owes **nothing** —
not even the base fee. Once there is at least one closed session the base fee
is earned (the supporter travelled and showed up) and time past the inclusion
bills normally. The shopping budget is never an input to a cancellation: it is
an authorization ceiling, not money anyone has been charged.

### 2. Shopping / purchases

| | |
|---|---|
| Budget | requester sets it at post. No preset default. **Cap $30.00**, enforced in Go (create, update, estimate) and by a DB `CHECK`. |
| Settlement | the **verified receipt amount**, capped at approved budget + **$5.00 tolerance** |
| Overage ≤ $5 | auto-approved (the tolerance) |
| Overage > $5 | requires an in-app budget-increase request **before** the purchase |

The increase flow (Phase 2 UI): supporter requests, requester approves with one
tap, **5-minute timeout**, supporter pre-selects a fallback ("buy alternative
at $X" / "skip this item"). **Timeout = auto-DENY and the fallback executes.**

Reimbursement never exceeds approved budget + $5. Anything above that is the
supporter's own cost. **There is no after-the-fact charging, ever.**

`tasks.prepay_amount_cents` stays the requester's original ask;
`tasks.shopping_budget_approved_cents` is the currently-approved ceiling, which
starts equal to it and is what an approved increase raises. Keeping them
separate is what makes "was this overage approved, and when" answerable later.

### 3. Authorization and capture (Phase 2)

```
pre-auth at post  = (base_fee + estimated time cost) × 1.5 + budget + $5.00
capture at done   = time_cost + verified receipt amount
```

The multiplier applies to the **whole** time-based estimate, base fee included.
Applying it to the per-minute portion alone does not survive an example: a
30-minute task estimates $12.00 + $7.50 = $19.50, and 1.5 × $7.50 + $5.00 =
$16.25 would be a hold too small to cover a task that ran exactly to estimate.
`TestBillingPreAuthCoversTheHappyPath` asserts hold ≥ on-estimate capture
across the preset durations.

`CaptureMethod` is **manual**. The uncaptured remainder of the hold
auto-releases — **that is how "unused time is refunded" works, and it involves
no refund.** The happy path never issues one.

Auto-extend consent (cap **+15 min**) is captured at post as
`tasks.auto_extend_consent` (default `true`).

### 4. Payouts (Phase 3)

Destination charge. During beta the supporter receives **100%** of time cost
and the full receipt amount; `application_fee_amount` is parameterized via
`BillingConfig.ApplicationFeeBasisPoints`, **0 for now**.

### 5. Where it lives

| Concern | Location |
|---|---|
| Every constant | `BillingConfig` — `server/billing.go` |
| Quote + settlement math | `quoteTask`, `calcTaskCostCents`, `cancelSettlementCents` — `server/billing.go` |
| Quote endpoint | `POST /tasks/estimate` → `estimateTaskCost` |
| Settlement breakdown | `GET /tasks/:id/worklogs` → `cost` object |
| Stripe calls | `CreatePreAuth` / `Capture` / `Release` — `server/payments.go` |
| Stripe events | `POST /webhooks/stripe` — `server/stripe_webhook.go` |
| Ledger | `public.payments`, `public.stripe_webhook_events` |
| Client rule | display only; `app/src/lib/pricing.test.mjs` fails the build on any local price math |

**Currency** is USD (`BillingConfig.Currency = "usd"`), single-currency by
assumption. All money is **integer cents** end to end; the only float is in
`formatCentsUSD` / `formatCents`, at the last step before a string.

### 6. Payments tables

`public.payments` — one row per Stripe PaymentIntent, one per task in the
Phase 2 happy path.

| Column | Notes |
|---|---|
| `kind` | `task_payment` today; Phase 2 adds `budget_increase` |
| `status` | `requires_auth` → `authorized` → `captured`, or `canceled` / `failed` |
| `authorized_cents` | what the hold is for |
| `captured_cents`, `time_cost_cents`, `shopping_receipt_cents` | fill in at settlement, so a captured row carries the full split of what was paid and why — which is what a dispute needs |

A partial unique index on `(task_id, kind) WHERE status IN ('requires_auth','authorized')`
makes a duplicate live hold impossible rather than merely unlikely.

`public.stripe_webhook_events` — `event_id` primary key. Stripe delivers at
least once and retries for days on any non-2xx, so the handler claims the event
id with `INSERT … ON CONFLICT DO NOTHING` before doing any work; a duplicate is
acknowledged and dropped.

Both tables ship with **RLS enabled and zero policies** (deny-all, S-10). No
client role holds any grant. `payments` is the one table where a client-direct
read would expose charge history, so this is not boilerplate there.

### 7. Stripe webhook

`POST /webhooks/stripe`, unauthenticated by design — the signature *is* the
authentication, verified over the **raw request body** (Gin's JSON binding
would re-encode it and break verification) with `webhook.ConstructEvent`, which
also enforces Stripe's 5-minute timestamp tolerance against replay.

Fail-closed: no `STRIPE_WEBHOOK_SECRET` → **401**, same as the TalkJS webhook.
Unconfigured payment routes → **503** via `requirePaymentsEnabled()`.

| Event | Handling |
|---|---|
| `payment_intent.succeeded` | record capture; amount taken from Stripe, the authority on what moved |
| `payment_intent.canceled` | mark the hold released |
| `payment_intent.payment_failed` | mark the hold dead |
| `charge.dispute.created` | `audit_logs` row (`PAYMENT_DISPUTED`) **and** email to the ops allowlist |
| anything else | logged, `200`, ignored — a non-2xx would make Stripe retry for days and eventually disable the endpoint |

A dispute has a response deadline in days and loses by default if missed, so it
gets both a durable row and an email; neither alone is sufficient. A dispute
that cannot be traced to a task is filed against the nil UUID rather than
dropped — an untraceable dispute is *more* urgent, not less.

### 8. Environment

| Variable | Purpose |
|---|---|
| `STRIPE_SECRET_KEY` | test key (`sk_test_…`) for now. Absent → payment routes 503, `payments.go` returns `ErrPaymentsDisabled` |
| `STRIPE_WEBHOOK_SECRET` | endpoint signing secret (`whsec_…`). Absent → webhook 401 |

Startup logs whether payments are enabled and whether the key is test or live.
The key itself is never logged (S-12).
