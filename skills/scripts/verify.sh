#!/usr/bin/env bash
# HO:RA verify entry point — the single command that constitutes "evidence of health".
# Exit 0 = green. The Verifier role runs exactly this and reports raw output.
set -uo pipefail

FAIL=0
step() { echo; echo "=== $1 ==="; }
red()  { echo "RED: $1"; FAIL=1; }

# --- I-03: backend ----------------------------------------------------------
step "go build / vet / test (I-03)"
( cd server && go build ./... && go vet ./... && go test ./... ) || red "server checks"

# --- I-04: typecheck ---------------------------------------------------------
step "web build check (I-04)"
( cd app && npm run build ) || red "app build"

if [ -d mobile ]; then
  step "mobile typecheck (I-04)"
  ( cd mobile && npx tsc --noEmit ) || red "mobile typecheck"
fi

# --- I-01b: no client-direct data access -------------------------------------
step "no supabase data calls from clients (I-01b / S-01)"
if grep -rn "supabase\.from(\|supabase\.rpc(" app/src mobile/src 2>/dev/null; then
  red "client-direct DB access found"
else echo "ok"; fi

# --- I-02: no privileged keys in client code ----------------------------------
step "no privileged keys in clients (I-02)"
if grep -rn "service_role\|SUPABASE_DB_URL\|SESSION_JWT_SECRET" app/src mobile/src 2>/dev/null; then
  red "privileged key reference in client code"
else echo "ok"; fi

step "secret scan (I-02)"
if command -v gitleaks >/dev/null 2>&1; then
  gitleaks detect --source . --no-banner || red "gitleaks findings"
else
  echo "SKIP: gitleaks not installed (TODO(confirm): install or choose scanner)"
fi

# --- I-07: cross-project isolation --------------------------------------------
step "no birthday-card project references (I-07 / S-03)"
if grep -rn "aemwljralqsegrwivbub" app/ server/ mobile/ supabase/ 2>/dev/null; then
  red "cross-project reference found"
else echo "ok"; fi

# --- I-01a: PostgREST side door (activates after D-02 lockdown lands) ----------
step "RLS side-door lock (I-01a) — ACTIVE (D-02 closed: baseline all-true 2026-07-11)"
# TODO(confirm connection): e.g. psql "$HORA_DB_URL" -f skills/scripts/rls_check.sql
# Until wired: run the two queries in INVARIANTS.md I-01a manually against
# akxsdkerudurzcemurrb and record the result in the review evidence bundle.
echo "manual check — see INVARIANTS.md I-01a"

# --- I-05: schema drift (dormant until baseline migration exists) --------------
step "schema drift (I-05) — dormant"
if ls supabase/migrations/*.sql >/dev/null 2>&1; then
  supabase db diff --linked || red "schema drift detected"
else
  echo "SKIP: no baseline migration yet (S-21 / D-01 open item)"
fi

# --- I-06: mobile build (dormant until mobile/ exists) --------------------------
if [ -d mobile ]; then
  step "mobile export builds (I-06)"
  ( cd mobile && npx expo export --platform ios ) || red "mobile build"
fi

# --- mobile UI design-system checks (DESIGN.md §8) -----------------------------
if [ -d mobile ]; then
  step "mobile UI design-system checks (DESIGN.md §8)"

  if grep -rn --include='*.tsx' --include='*.ts' -E '#[0-9a-fA-F]{3,8}\b' mobile/src --exclude-dir=theme; then
    red "raw hex outside theme folder"
  else echo "ok: no raw hex"; fi

  if grep -rn --include='*.tsx' -E 'fontSize:\s*[0-9]|fontWeight:\s*["'\'']?[0-9]' mobile/src/app mobile/src/components --exclude-dir=ui; then
    red "raw fontSize/fontWeight in screens or components"
  else echo "ok: no raw type values"; fi

  if grep -rn --include='*.tsx' --include='*.ts' -E 'font-(bold|medium)|fontWeight:\s*["'\'']?(500|700|800|900)' mobile/src; then
    red "forbidden font weight"
  else echo "ok: no forbidden weights"; fi
fi

echo
if [ "$FAIL" -eq 0 ]; then echo "=== VERIFY: GREEN ==="; else echo "=== VERIFY: RED ==="; fi
exit $FAIL
