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

### 1.3 RLS Policies

**None exist on Hora MVP tables.** Authorization is enforced entirely at the Go application layer. The direct Postgres connection uses the `postgres` superuser role (via pooler), so Supabase RLS is bypassed.

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
  "transport_required": "none|..."
}
```

**`POST /tasks/:id/complete` body:** `{ "completion_photo_url": "string (required)", "completion_note": "string" }`

**`POST /tasks/:id/cancel` body:** `{ "reason": "string (required)" }`

**`POST /tasks/:id/estimate-travel` body:** `{ "supporter_lat": 40.7, "supporter_lng": -74.0 }`
Response: `{ "travel_minutes", "task_minutes", "total_minutes", "display" }`

**`POST /tasks/:id/review` body:** `{ "stars": 1-5, "value_rating": "not_worth|fair|great", "would_rehire": bool, "comment": "string" }`

**Pricing formula (computed in Go, not stored):**
- Base fee: $25.00 (companionship/companion), $18.00 (estimated > 90 min), $12.00 (everything else)
- Overtime: $0.50 per minute worked (applied to all minutes, not just beyond a threshold)
- `total_cost_cents = base_cents + total_minutes * 50`

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

7. **Pricing is computed in Go.** There is no `price` or `cost` column in `tasks`. Cost is always derived at query time by the Go backend from worklog duration. If you need to display cost in a new app, call the Go API or reproduce the formula: `base + total_minutes * 50`.

8. **`supporter_status` is derived.** There's no `supporter_status` column in the DB. It's computed: if `is_verified_supporter = true` → `"approved"`; else if `supporter_rejected_at IS NOT NULL` → `"rejected"`; else if `supporter_applied_at IS NOT NULL` → `"applied"`; else `"none"`. Rejection outranks the application timestamp because `supporter_applied_at` is never cleared (D-08).

9. **Admin allowlist is hardcoded in Go.** The ops admin emails are a static map in `main.go`. Adding a new admin requires a code change and redeployment.

10. **WhatsApp is not functional.** The webhook receiver is wired and logs incoming messages, but no outbound notifications are sent (all are TODO). `helpers.SendWhatsAppMessage` works if env vars are set, but nothing calls it in production paths yet.

11. **Edge Function `notify-new-task` may be a zombie.** The Go backend's `NotifyAdminNewTask()` already handles admin emails for new tasks. The Edge Function does the same thing. Check whether a DB webhook trigger is still pointing at it in the Supabase dashboard; if so, admins will receive duplicate emails.

12. **Session cookie vs. Bearer — CORS.** The `hora_session` cookie uses `SameSite=None; Secure` in prod (cross-domain). If your new app is on a different origin, you need `credentials: 'include'` on fetch calls and the origin must be in `CORS_ALLOW_ORIGINS` or `*.vercel.app`.
