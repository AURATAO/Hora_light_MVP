# HO:RA Mobile

Expo (React Native) client for HO:RA. Foundation phase: navigation shell, auth
flow, and Go backend wiring. No feature screens yet.

## Run

```
cd mobile
npx expo start
```

Then press `i` for the iOS simulator, `a` for Android, or scan the QR code
with Expo Go / a dev client.

## Environment

Copy `.env.example` to `.env` and fill in:

- `EXPO_PUBLIC_SUPABASE_URL` — `https://akxsdkerudurzcemurrb.supabase.co` (Hora MVP project; already filled in the example)
- `EXPO_PUBLIC_SUPABASE_ANON_KEY` — from the Supabase dashboard: Project Settings → API → `anon` `public` key, project `akxsdkerudurzcemurrb`

Never use the `service_role` key here — this is client code (S-10/S-11).

## Backend

All business data (tasks, profiles, notifications, ...) goes through the Go
backend at `https://core.horaapp.co`. The app never queries Supabase tables
directly — the only direct Supabase calls are `supabase.auth.*` (see
`src/lib/supabase.ts`) and, later, public `avatars` bucket reads. See
`src/lib/api.ts` for the shared `apiFetch` wrapper (always sends
`credentials: 'include'` so the `hora_session` cookie round-trips).

## Auth flow

Two sign-in methods, both finishing with the same exchange step:

1. **Google OAuth** — `supabase.auth.signInWithOAuth({ provider: 'google' })`
   opens Google's consent screen in an in-app browser
   (`expo-web-browser` + PKCE). The callback URL comes back with a `?code=`
   param, exchanged via `supabase.auth.exchangeCodeForSession`.
2. **Email code** — matches the web app: `supabase.auth.signInWithOtp({ email })`
   emails a 6-digit code (no magic link); the user enters it and the app calls
   `supabase.auth.verifyOtp({ email, token: code, type: 'email' })`.

In both cases, once expo-router's login screen (`src/app/(auth)/login.tsx`)
has a Supabase session, it calls `POST /auth/exchange` with the Supabase
`access_token`. The Go backend sets the `hora_session` cookie and returns
`{ auth, id, email, name }`; the app stores `id`/`email` in SecureStore and
navigates to `/(tabs)/home`.

The root layout (`src/app/_layout.tsx`) checks `GET /auth/me` on launch and
routes to the tab navigator or the login screen accordingly, and re-checks on
any Supabase auth state change.

**Identity note:** after exchange, always use the internal `id` the Go
backend returns for API calls — it is not the same UUID as the Supabase
`auth.users.id`.
