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

The complete pricing, settlement and payment model.

Phase 1 shipped the billing engine, the payments schema and the Stripe webhook
with nothing calling them. **Phase 2a (§9)** wired the first half in: a card on
file, a pre-auth at post, and a release on every cancel path. **Phase 2b (§10)**
closes the loop: capture at completion, receipts, mid-task budget and time
approvals, and the three-layer time cap. All of it is behind
`PAYMENTS_ENFORCED`, which is **off** in production — with the flag off, no
task has a hold, nothing is captured and none of the money paths below execute.
Payouts to supporters are Phase 3 and still have config support with no code
path that reaches them.

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

The increase flow (§10.2): supporter requests, requester approves with one tap,
**5-minute timeout**, supporter pre-selects a fallback ("buy alternative" with a
note / "skip this item"). **Timeout = auto-DENY and the fallback executes.**

Reimbursement never exceeds approved budget + $5. Anything above that is the
supporter's own cost. **There is no after-the-fact charging, ever.**

`tasks.prepay_amount_cents` stays the requester's original ask;
`tasks.shopping_budget_approved_cents` is the currently-approved ceiling, which
starts equal to it and is what an approved increase raises. Keeping them
separate is what makes "was this overage approved, and when" answerable later.

### 3. Authorization and capture (Phase 2)

```
pre-auth at post  = (base_fee + estimated time cost) × 1.5 + budget + $5.00
                    + $5.00 overage tolerance, when budget > 0
capture at done   = time_cost + verified receipt amount
```

That last term is a **Phase 2b correction**. `PreAuthBufferCents` was standing in
for both the $5 auto-approved overage tolerance *and* the auto-extend headroom —
the same $5 counted twice — so a short shopping task held less than its ceiling
settlement: a 15-minute delivery with a $15 budget held $38.00 against a
settlement of $39.50. Undercapture of up to $1.50 per such task, silent except
for one log line. `TestPhase2bPreAuthCoversCappedSettlement` sweeps every
duration and both tiers against the ceiling settlement and is what found it; the
no-budget hold is unchanged.

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
| Quote + settlement math | `quoteTask`, `quoteSettlement`, `calcTaskCostCents`, `cancelSettlementCents` — `server/billing.go` |
| The billable-time ceiling | `taskTimeCapMinutes`, `cappedMinutes`, `timeCostCentsCapped` — `server/billing.go` |
| Quote endpoint | `POST /tasks/estimate` → `estimateTaskCost` |
| Settlement breakdown | `GET /tasks/:id/worklogs` → `cost` + `settlement` objects |
| Stripe calls | `CreatePreAuth` / `Capture` / `Release` — `server/payments.go` |
| Post-time wiring | `payments_preauth.go` (flag, decline mapping, release, 3DS confirm) |
| Settlement + multi-hold capture | `payments_settlement.go` |
| Mid-task asks | `extensions.go` (`/tasks/:id/budget-increase`, `/time-extension`, `/extensions/*`) |
| The three-layer time cap | `timecap.go` |
| Card on file | `payments_cards.go` (`/payments/*`) |
| Stripe events | `POST /webhooks/stripe` — `server/stripe_webhook.go` |
| Ledger | `public.payments`, `public.stripe_webhook_events`, `public.extension_requests` |
| Ops procedures | `skills/payments-runbook.md` |
| Client rule | display only; `app/src/lib/pricing.test.mjs` fails the build on any local price math |

**Currency** is USD (`BillingConfig.Currency = "usd"`), single-currency by
assumption. All money is **integer cents** end to end; the only float is in
`formatCentsUSD` / `formatCents`, at the last step before a string.

### 6. Payments tables

`public.payments` — one row per Stripe PaymentIntent, one per task in the
Phase 2 happy path.

| Column | Notes |
|---|---|
| `kind` | `task_payment`, or `budget_increase` — a supplementary hold opened when an approved increase outgrew the original one |
| `status` | `requires_auth` → `authorized` → `captured`, or `canceled` / `failed` (the hold never landed) / `capture_failed` (the hold landed, the task completed, the capture did not go through) |
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
| `payment_intent.canceled` | mark the hold released; sync the task (§9.5) |
| `payment_intent.payment_failed` | mark the hold dead; sync the task (§9.5) |
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
| `STRIPE_PUBLISHABLE_KEY` | `pk_test_…`. Sent to both clients in every payments response, so rotating it needs no web deploy and no native rebuild. Public by design (S-12) |
| `PAYMENTS_ENFORCED` | `1/true/yes/on` → posting requires a card and places a hold. **Anything else, including unset, is off** |
| `STRIPE_INCREMENTAL_AUTH` | `1/true/yes/on` → the pre-auth requests incremental-authorization support and an approved budget increase grows the existing hold. **Off by default, and turning it on before Stripe enables flexible payments on the account breaks every post** — see §10.3 |

Startup logs whether payments are enabled and whether the key is test or live.
The key itself is never logged (S-12).

---

### 9. Phase 2a — card on file and the pre-auth

#### 9.1 The flag

`PAYMENTS_ENFORCED` is read on every call, not cached at startup, and defaults
**off**. With it off, `POST /tasks` behaves exactly as it did before Phase 2a:
no card required, no hold, no `payments` row, `tasks.payment_id` null. Every
payment branch in `createTask` is downstream of `paymentsEnforced()`.

Enforcement **on** with no `STRIPE_SECRET_KEY` is a misconfiguration and
refuses every post with **503** — it never falls through to the unenforced
path, which would post tasks nobody is holding money for while an operator
believes payments are live.

#### 9.2 Card-on-file endpoints

All session-authenticated, all behind `requirePaymentsEnabled()` (503 when
Stripe is unconfigured). The client never names a Stripe customer; it is always
resolved from the session uid via `users.stripe_customer_id`.

| Endpoint | Returns |
|---|---|
| `POST /payments/setup-intent` | `client_secret`, `customer_id`, `ephemeral_key`, `publishable_key`, `merchant_display_name` — everything PaymentSheet needs in setup mode, in one call |
| `GET /payments/payment-methods` | `cards[]` (`id`, `brand`, `last4`, `exp_month`, `exp_year`, `is_default`), `has_card`, `publishable_key`, `payments_enforced` |
| `DELETE /payments/payment-methods/:id` | `{ok:true}`; **404** for a card the session does not own (confirming it exists is itself the leak); **409** when it is the charging card and the requester has a live hold |

`users.stripe_customer_id` is created **lazily** on first card-sheet open, not
at signup — most beta accounts never add a card, and a Customer per signup is a
permanent row in Stripe's database for a user who never transacts. The persist
is `coalesce(stripe_customer_id, $2)` so a race keeps one id and discards the
other.

The SetupIntent is created with `usage: off_session`. A card saved `on_session`
produces a PaymentMethod the issuer expects a challenge for on every use, which
would turn every post into a 3DS prompt.

#### 9.3 Posting with the flag on

```
resolve card (no card → 402 payment_method_required, nothing written)
  insert task  status = 'pending_payment'          ← in no feed, no list
    CreatePreAuth  (row written BEFORE the Stripe call)
      authorized          → task 'open' + tasks.payment_id   → 201
      authentication_required → task stays parked            → 402 + client_secret
      declined / anything else → both rows deleted           → 402 + message
```

**Why `pending_payment` exists.** `payments.task_id` is `NOT NULL`, so the hold
cannot be recorded before the task row exists; and `payments.go` writes its row
*before* calling Stripe on purpose, because a live hold with no row naming it
is unrecoverable where a row naming an intent that may not exist is merely
untidy. Those two force task → payment row → Stripe call, which would leave an
`open`, unfunded, acceptable task in the supporters' feed for the second the
authorization takes. `pending_payment` closes that window by construction: the
task becomes `open` in the same statement that records the authorization.

Excluded from `listMyTasks`, `listProfileTasks` and every feed (those already
filter `status='open'`). A stranded row means a 3DS challenge nobody finished;
the sweep query is at the bottom of the Phase 2a migration.

#### 9.4 3DS / SCA

An off-session confirm that the issuer wants the cardholder present for comes
back from stripe-go as an **error carrying a good PaymentIntent**. Read as a
decline it would discard a recoverable attempt and tell the requester their
card was refused. `classifyPreAuthError` separates the two; the 402 carries
`client_secret`, `payment_intent_id`, `task_id` and `publishable_key`.

The client runs the challenge with its Stripe SDK and then calls
**`POST /tasks/:id/payment/confirm`**, which **re-reads the intent from Stripe**
rather than believing the client:

| Intent status | Result |
|---|---|
| `requires_capture` | row → `authorized`, task → `open`, ops announcement fires |
| `requires_action` / `requires_confirmation` | 402 again; task stays parked |
| anything else | both rows discarded; 402 |

Idempotent: a task already `open` answers 200.

#### 9.5 Hold lifecycle

| Event | Hold |
|---|---|
| requester cancels (`POST /tasks/:id/cancel`) | released; `PAYMENT_RELEASED` audit row |
| admin cancel / admin remove | released; same audit row |
| **reassign** | **untouched** — the hold is the requester's money for the same task at the same price |
| task edit that would need a bigger hold | **409 `exceeds_authorized_hold`**, naming the held amount. Re-authorizing for more is Phase 2b |
| `payment_intent.canceled/payment_failed` on a `pending_payment` task | both rows discarded |
| the same on an **open** task | task stays posted; `PAYMENT_HOLD_LOST` audit row + ERROR log |

Release is **best-effort and runs after the status write**: a Stripe outage
must not be able to stop somebody cancelling their own task. A failure writes
`PAYMENT_RELEASE_FAILED` to `audit_logs` and logs `[STRANDED HOLD]`; the hold
expires on its own within a week regardless.

An `open` task whose hold is lost is deliberately **not** un-posted — a
supporter may already be travelling, and cancelling out from under them is
worse than a settlement handled by hand.

#### 9.6 Pre-auth expiry — known limitation

Card networks release an uncaptured manual-capture authorization after about
**7 days**. `watchExpiringPreAuths` runs every 6 hours and **logs only**:

```
[payments][EXPIRING HOLD] task=… payment=… intent=… amount=$76.75 age=6.2d
```

for any `open` task with an `authorized` hold older than 6 days. Re-authorizing
would be a second charge attempt on a week-old task that can itself decline,
can double-hold if the first release lags, and needs a requester-facing story
when it fails — it is a later refinement, not part of the phase that first
places a hold. What happens today if a hold does lapse: `Capture` fails loudly
at completion rather than silently, which is why the warning exists — so an
operator sees the task before a supporter works it.

#### 9.7 Auto-extend consent

`tasks.auto_extend_consent` (default **true**) is now set from the post form.
An **absent** field means unchanged, not refused — web and every shipped mobile
build send nothing, and reading their silence as a denial would mark them all
as having declined something they were never asked.

The pre-auth amount deliberately does **not** depend on it. With
`held = 1.5(B+T) + $5` and `capture = B + T + 15 × $0.50`, the margin is
`0.5(B+T) + $5 − $7.50 ≥ $3.50` at every duration and both base-fee tiers,
since `B ≥ $12.00`. `TestPreAuthCoversAutoExtend` pins that floor.

---

### 10. Phase 2b — settlement, receipts, mid-task approvals, the time cap

Everything from here is behind `PAYMENTS_ENFORCED` unless it says otherwise.
The billable-time ceiling (§10.4) is the exception: it is part of the pricing
model, not of payments, and it applies whether or not anybody is being charged.

#### 10.1 Capture at completion

```
completeTask
  validate the receipt          → 400 before the task is touched, if it fails
  task → 'completed', receipt columns written
  settleTaskPayment             → capture time cost + verified receipt
    ok      → tasks.settled_* written, PAYMENT_CAPTURED audit row
    error   → payments.status = 'capture_failed', audit row, ops emailed
```

**A capture failure never blocks a completion.** The task completes, the row
lands in `capture_failed`, `audit_logs` gets `PAYMENT_CAPTURE_FAILED` carrying
`owed_cents`, and the ops allowlist gets an email. A supporter standing in
someone's kitchen must not be unable to close a finished task because a card
network is having a bad afternoon; settling by hand afterwards is cheap.

The same path runs on **admin force-complete** (a force-complete *is* a
completion — somebody worked and is owed) and on **cancel-with-sessions**
(§10.5).

**Multi-hold settlement.** A task may carry a second `budget_increase` hold
(§10.3). Capture takes the main hold first, then supplementary holds oldest
first until the total is covered, then releases whatever is left — an unused
authorization is real money frozen on a real card.

**`adjust-time` after a capture is refused** with **409 `already_captured`**,
naming the capture date and the settled amount. Before a capture it simply
re-prices, because nothing is stored: every settlement figure derives from the
worklogs at the moment it is asked for. Correcting a capture is a refund, and
refunds are a manual Stripe-dashboard operation during beta —
`skills/payments-runbook.md` is the procedure.

#### 10.2 Receipts

On a task with `shopping_budget_approved_cents > 0` the completion **requires**
an answer about the receipt. `receipt_amount_cents` is a pointer server-side so
that "no receipt field was sent" stays distinguishable from "the receipt was
zero" — silence from a client that knows nothing about receipts must not be read
as `$0` on a task somebody shopped for.

| Condition | Answer |
|---|---|
| budget > 0, field absent | **400 `receipt_required`** |
| amount > 0, no photo | **400 `receipt_photo_required`** |
| amount > approved budget + $5 | **400 `receipt_exceeds_budget`**, carrying `max_receipt_cents` and a message pointing at a budget increase |
| amount = 0 | fine — nothing was bought, shopping component is $0, no photo needed |

Refused **before** the task is marked complete, deliberately: an over-budget
receipt is the one completion failure the supporter can fix, and a completed
task offers neither a correction nor an increase.

The receipt photo uploads through the existing `POST /tasks/:id/completion-photo`
endpoint and the same `task-completions` bucket — same kind of file, same phone,
same moment. Both parties see it on the settlement; only the requester is
offered "report a problem", which is a `mailto` to support with the task id, not
a dispute system.

#### 10.3 Mid-task asks — `public.extension_requests`

One table, `kind IN ('budget','time')`. See **D-11** for why it is one table and
not two. `requested_cents` / `requested_minutes` are the **additional** amount,
never a new total.

```
POST /tasks/:id/budget-increase   supporter only, task open + assigned
POST /tasks/:id/time-extension    ditto; minutes must be one of time_choices
GET  /tasks/:id/extensions        both parties — and applies the expiry
POST /tasks/:id/extensions/:eid/approve|deny   requester only
```

**Expiry is lazy, and the read is the whole scheduler.** A request older than
`ApprovalTimeoutMinutes` (5) is expired by whoever looks at it next — the
supporter's polling screen, the requester opening the notification, the resolve
handler itself, or the 6-hourly sweep that already watches pre-auths. Both
clients poll `/extensions` every 5s while a task is live, so the timeout lands
within seconds for the one person actually waiting on it, and nothing runs at
all for the tasks where nobody is.

**Silence is a denial**, which is why a budget request must carry a fallback
(`buy_alternative` with a note, or `skip_item`) chosen up front: the supporter
is stood in a shop and needs to act on silence, not decide under time pressure.
A time request needs no fallback — the fallback is the billing, since time past
the ceiling is simply not charged.

**One pending per task per kind** (partial unique index; 409 on a second of the
same kind). Per kind rather than per task: a supporter waiting on "can I spend
$8 more" must still be able to say "and I need 15 more minutes".

**Approval moves the ceiling first, then the hold.** If growing the hold fails,
the approval still stands — the requester said yes and the supporter may spend;
the platform carries the gap and it surfaces at capture. Which mechanism grows
the hold:

| | |
|---|---|
| **Incremental authorization** | Preferred — one hold, one statement line. Requires `STRIPE_INCREMENTAL_AUTH=1` **and** a Stripe account enabled for flexible payments. |
| **Supplementary payment** | A second PaymentIntent (`kind='budget_increase'`) for the shortfall. Every card supports it. **This is the live path.** |

Requesting incremental authorization on an ineligible account is **not** a soft
degradation: the off-session confirm fails with
`payment_intent_invalid_parameter` — *"This account is not eligible for the
requested card features"* — the intent holds nothing, and every post 402s.
Verified against the real test API on 2026-09-14. Hence the flag, default off,
and hence `ensureHoldCoversTask` not attempting an increment while it is off
(an intent created without the capability can never be incremented, so the call
would be guaranteed-to-fail latency inside a requester's Approve tap).

#### 10.4 The billable-time ceiling, and the three layers

```
cap = estimated_minutes
    + AutoExtendMinutes (15) if tasks.auto_extend_consent
    + every minute of every approved time extension
```

**The cap bounds TOTAL LOGGED MINUTES, not billable minutes**, and the order
matters: capping billable minutes would hand the requester the 15-minute
inclusion a second time. On a 45-minute ceiling with 60 minutes logged, the
right answer is `billable(min(60,45)) = 30`; capping billable would give 45 —
more than the ceiling itself is worth.

| Layer | When | What |
|---|---|---|
| **2 — warning** | logged ≥ cap − `CapWarningLeadMinutes` (5) | Both parties told, once. Supporter gets one-tap "+15 / +30"; requester gets the mirror. Fires whether or not consent was given — the point is that nobody is surprised. |
| **1 — auto-extend** | logged ≥ cap | Billing stops accruing. With consent the ceiling is estimate+15, so those minutes bill with no interruption at all. Both told; `TIME_CAP_REACHED`. |
| **3 — unresponsive** | `GracePeriodMinutes` (30) past the cap, no approval, nothing pending | Ops emailed; `TIME_CAP_UNRESPONSIVE` audit row. |

**No automatic cancellation, ever**, at any layer. The task stays completable
and settles at the capped amount. The supporter's screen says the meter has
stopped, never that they must.

Each layer fires **once per task**, enforced by the database
(`UPDATE … WHERE <latch> IS NULL RETURNING`) rather than by a flag in memory, so
two GPS pings in the same second produce one notification. An approved time
extension clears all three latches, so the sequence repeats cleanly against the
new ceiling.

**Where it is evaluated — no new scheduler.** `POST /tasks/:id/gps-ping` is the
real heartbeat: it arrives every 30s from the supporter's phone while they are
clocked in, including with the screen locked, which is exactly the situation the
warning exists for. Also on `GET /tasks/:id/worklogs`, on clock-out, and in the
existing 6h watcher as a backstop for a supporter who went dark.

#### 10.5 Cancel with closed sessions

The branch Phase 1 built `cancelSettlementCents` for and left unreachable.

| | |
|---|---|
| unaccepted, or accepted with **no** closed session | unchanged — still refused after acceptance, still $0 and a full release before it |
| accepted with **≥1 closed session** | now allowed. Settlement = base fee + billable minutes of the closed sessions, **captured** against the hold; the remainder auto-releases |

The supporter is **detached** (the task is over; leaving them assigned to a
cancelled job keeps it showing up as theirs) and told what they earned, by the
id read *before* the detach — `notifyAssignee` re-reads the task to find its
recipient and would find nobody.

Detaching used to mean losing access: `getTask` authorized on
`assigned_to_id` alone, so the person who had just been paid could no longer
open the task and see what for. Both `getTask` and `getWorklogs` now also admit
**anyone who logged time on the task** (matched on the worklog's email column),
which closes the same hole for admin-removed and reassigned tasks too.

#### 10.6 The settlement payload

`GET /tasks/:id/worklogs` gained a `settlement` object and `cost` gained
`billed_minutes`, `cap_minutes` and `shopping_receipt_cents`. Extended rather
than given a parallel endpoint: "what does this cost so far" and "what was I
charged" are one question asked at two moments, and two endpoints would be two
chances to answer them differently.

`settlement.state` is the one field a client branches on:

| | |
|---|---|
| `not_charged` | no hold was ever placed — the figures are what *would* be charged. Every task in the running beta. |
| `estimated` | still running; the bill so far |
| `captured` | the money moved; `captured_cents` and `settled_at` are present |
| `capture_failed` | finished, charge did not go through. Ops have been emailed; **nothing for either party to do in-app**, and both clients say exactly that rather than raising an alarm |

#### 10.7 Notification types added

`BUDGET_INCREASE_REQUESTED`, `TIME_EXTENSION_REQUESTED`, `EXTENSION_RESOLVED`,
`TIME_CAP_WARNING`, `TIME_CAP_REACHED`. All five deep-link to the task screen,
which is where both the approve/deny card and the supporter's own request live —
there is no separate approval screen.

`EXTENSION_RESOLVED` covers approved / denied / expired in one value, the same
way `TASK_REASSIGNED` covers three recipients: the outcome is composed in Go and
the email template renders what it is handed, so three near-identical values
would be three places to forget.

`notifications.type` is an enum — **these had to land in the database before the
Go build that emits them** (S-21). `TestAdminRemoveNotificationEnumCoversEveryEmittedType`
is the regression net; it also gained `TASK_REASSIGNED`, which had been emitted
since September and was missing from the list.
