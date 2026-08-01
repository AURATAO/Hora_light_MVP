# HO:RA MVP

Minute-billing urban support platform. React frontend + Go backend + Supabase (Postgres + Auth + Storage + Edge Functions).

---

## Stack

| Layer | Tech |
|---|---|
| Frontend | React 18, Vite, Tailwind CSS 4 |
| Backend | Go (Gin), cookie-based session auth |
| Database | Supabase (Postgres) |
| Auth | Google OAuth via Go backend |
| Storage | Supabase Storage (avatars bucket) |
| Email | Postmark |
| Edge Functions | Supabase Edge Functions (Deno) |

---

## Project Structure

```
Hora_light_MVP/
├── app/              # React frontend
├── server/           # Go backend
└── supabase/
    └── functions/
        └── notify-new-task/   # Edge function: email on new task
```

---

## Local Development

### 1. Backend (Go)

```bash
cd server
go run ./
```

Runs on `http://localhost:8080`

**Required: `server/.env`**

```env
SUPABASE_PROJECT_URL=https://<ref>.supabase.co
SUPABASE_JWKS_URL=https://<ref>.supabase.co/auth/v1/jwks
SUPABASE_DB_URL=postgresql://postgres:<password>@db.<ref>.supabase.co:5432/postgres
SUPABASE_JWT_SECRET=
SUPABASE_SERVICE_ROLE_KEY=
AVATARS_BUCKET=avatars

SMTP_HOST=smtp.postmarkapp.com
SMTP_PORT=587
SMTP_USER=
SMTP_PASS=                    # Postmark server token
EMAIL_FROM=no-reply@horaapp.co
APP_BASE_URL=http://localhost:5173

OAUTH_REDIRECT_URL=http://localhost:8080/auth/google/callback
GOOGLE_CLIENT_ID=
GOOGLE_CLIENT_SECRET=

CORS_ALLOW_ORIGINS=http://localhost:5173
GIN_MODE=debug
APP_ENV=development
COOKIE_SECURE=false
TZ=America/New_York
```

**Optional: App Review bypass** (`server/review_account.go`)

Set both of these and one single account can sign in with a fixed code instead
of a Supabase OTP, so Apple's reviewers can get into a TestFlight / App Store
build without an inbox. Leave either unset — the normal state — and the
`/auth/review-login` route is not registered at all. Every other account's OTP
is untouched either way. **Unset these once App Review approves the build.**

```env
REVIEW_ACCOUNT_EMAIL=info@my-hora.com
REVIEW_ACCOUNT_CODE=              # six digits; the live value lives in Render only

# optional — the profile the account is seeded with on first login
REVIEW_ACCOUNT_NAME=Hora Review
REVIEW_ACCOUNT_PHONE=+1 212 555 0100
REVIEW_ACCOUNT_CITY=New York
REVIEW_ACCOUNT_AVATAR_URL=          # empty → the reviewer picks a photo once
```

---

### 2. Frontend (React)

```bash
cd app
npm install       # first time only
npm run dev
```

Runs on `http://localhost:5173`

**Required: `app/.env.local`**

```env
VITE_API_BASE=http://localhost:8080
VITE_API_BASE_URL=http://localhost:8080
VITE_AUTH_MODE=cookie

VITE_SUPABASE_URL=https://<ref>.supabase.co
VITE_SUPABASE_ANON_KEY=

VITE_GOOGLE_MAPS_API_KEY=
VITE_TALKJS_APP_ID=
```

---

## Database (Supabase)

All schema lives in Supabase Postgres. Key tables:

| Table | Purpose |
|---|---|
| `public.profiles` | User profiles (phone, avatar, beta_accepted) |
| `public.tasks` | Task listings |
| `public.notifications` | In-app notifications |

### Required migrations

Run these once in **Supabase SQL Editor** if setting up fresh:

```sql
-- Beta acceptance flag
ALTER TABLE public.profiles
  ADD COLUMN IF NOT EXISTS beta_accepted boolean NOT NULL DEFAULT false;

-- Enable pg_net for edge function triggers
CREATE EXTENSION IF NOT EXISTS pg_net WITH SCHEMA extensions;

-- Trigger: email notification on new task
CREATE OR REPLACE FUNCTION notify_new_task()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM net.http_post(
    url := 'https://<YOUR_PROJECT_REF>.supabase.co/functions/v1/notify-new-task',
    headers := jsonb_build_object(
      'Content-Type',  'application/json',
      'Authorization', 'Bearer ' || current_setting('app.service_role_key', true)
    ),
    body := jsonb_build_object('record', row_to_json(NEW))
  );
  RETURN NEW;
END;
$$;

CREATE TRIGGER on_task_inserted
  AFTER INSERT ON public.tasks
  FOR EACH ROW EXECUTE PROCEDURE notify_new_task();
```

---

## Edge Functions (Supabase)

### notify-new-task

Sends a Postmark email to `liang.you@horaapp.co` when a task is created.

**Deploy:**

```bash
supabase functions deploy notify-new-task --no-verify-jwt
```

**Set secret:**

```bash
supabase secrets set SMTP_PASS=<postmark_server_token>
```

---

## User Onboarding Flow

1. **Google Login** → Go backend issues session cookie
2. **BetaModal** → shown once per account, requires checkbox to proceed
3. **Profile gate** → redirects to `/profile` until phone + avatar are filled
4. **Dashboard** → full access

---

## Build for Production

```bash
# Frontend
cd app
npm run build       # outputs to app/dist/

# Backend
cd server
go build -o hora ./
./hora
```
