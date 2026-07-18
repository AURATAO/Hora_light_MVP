# HO:RA Mobile

Expo (React Native) client for HO:RA. Foundation phase: navigation shell, auth
flow, and Go backend wiring. No feature screens yet.

## Run

This app uses a **custom dev client** (`expo-dev-client`), not Expo Go — it
already depends on native modules (`react-native-reanimated`,
`expo-notifications`, etc.) that Expo Go doesn't ship. You need a dev-client
build installed on your simulator/device once, then day-to-day work is just
Metro:

```
cd mobile
npx expo start --dev-client
```

Press `i` to launch on the iOS simulator, `a` for Android, or scan the QR
code with the installed dev client. If no dev client is installed yet, do a
local build first (see below) — `expo start --dev-client` only starts
Metro, it doesn't build/install the app.

### When you need a full rebuild vs. a plain reload

- **Plain reload (fast, automatic)** — editing JS/TSX, styles, or anything
  under `src/`. Metro Fast Refresh pushes the change straight to the running
  dev client, no rebuild needed.
- **Full rebuild required** — installing/removing a native module (any
  package with native code, or one whose `npx expo install` adds an
  `ios`/`android`/`plugins` entry to `app.json`), changing `app.json`
  `plugins`/permissions, or bumping the Expo SDK. Re-run:
  ```
  npx expo run:ios     # rebuilds native project + installs on simulator
  npx expo run:android
  ```
  `run:ios`/`run:android` do a full prebuild (regenerating the gitignored
  `ios/`/`android/` folders) + native compile, so the first one after
  cloning or after a native-dependency change takes several minutes
  (CocoaPods install + Xcode build). Subsequent `expo start --dev-client`
  sessions reuse that installed build until the native layer changes again.

### First-time local setup (once per machine)

- Xcode + an iOS Simulator (for `run:ios`)
- CocoaPods: `brew install cocoapods`
- Already logged in to EAS/Expo (`eas whoami`); if not, `eas login`

### Cloud builds (EAS)

`eas.json` defines build profiles for when a local Xcode toolchain isn't
available (CI, non-Mac machine, sharing a build with someone else):

- `development` — dev-client build for the iOS **simulator**
- `development-device` — same, but for a physical iOS **device** (requires
  a registered device + Apple Developer credentials; `eas build` walks you
  through provisioning interactively)
- `preview` — store-signed build for later TestFlight distribution (not
  used yet)

```
eas build --profile development --platform ios        # simulator, cloud-built
eas build --profile development-device --platform ios # physical device
```

The project is linked to EAS as `@taoaura/hora` (see `extra.eas.projectId`
in `app.json`).

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
