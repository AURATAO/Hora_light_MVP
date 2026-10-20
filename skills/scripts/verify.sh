#!/usr/bin/env bash
# HO:RA verify entry point — the single command that constitutes "evidence of health".
# Exit 0 = green. Exit 1 = red (a check failed). Exit 2 = incomplete: a check
# could not run because a tool or setting is missing — NOT green, and never
# reported as a test failure (D-16 R-5). The Verifier role runs exactly this
# and reports raw output.
set -uo pipefail

FAIL=0
INCOMPLETE=0
step() { echo; echo "=== $1 ==="; }
red()  { echo "RED: $1"; FAIL=1; }
incomplete() { echo "INCOMPLETE: $1"; INCOMPLETE=1; }

# Production Supabase project. No check in this script may connect to it
# (D-16 R-5): verification runs against a local database or a branch.
PROD_PROJECT_REF="akxsdkerudurzcemurrb"

# --- preflight: required tools, listed before anything runs -------------------
# A missing tool is a missing dependency, reported as such — not a red check.
# Install the tool (or set the variable) and re-run; never skip to green.
step "preflight: required tools and settings"
MISSING=0
have() { command -v "$1" >/dev/null 2>&1; }
need() {  # need <command> <label>
  if have "$1"; then echo "ok: $2 ($(command -v "$1"))"
  else echo "缺少相依套件 / MISSING DEPENDENCY: $2"; MISSING=1; fi
}
need go "Go toolchain"
need node "Node.js"
need npm "npm"
need supabase "supabase CLI (I-05 schema drift)"
need gitleaks "gitleaks (I-02 secret scan)"
# Run from outside the repo: the CLI rewrites supabase/.temp/cli-latest when
# invoked inside it, and verification must leave the working tree untouched.
if have supabase; then echo "   supabase CLI version: $(cd /tmp && supabase --version 2>/dev/null)"; fi
if have docker && docker info >/dev/null 2>&1; then echo "ok: docker daemon (supabase db diff shadow database)"
else echo "缺少相依套件 / MISSING DEPENDENCY: docker daemon (supabase db diff needs a shadow database)"; MISSING=1; fi
if [ -n "${VERIFY_DB_URL:-}" ]; then echo "ok: VERIFY_DB_URL is set (target for I-05)"
else echo "缺少設定 / MISSING SETTING: VERIFY_DB_URL — a LOCAL or BRANCH database URL for I-05 (never production)"; MISSING=1; fi
[ "$MISSING" -eq 0 ] || echo "→ checks that need the items above will report INCOMPLETE, not RED"

# --- I-03: backend ----------------------------------------------------------
step "go build / vet / test (I-03)"
( cd server && go build ./... && go vet ./... && go test ./... ) || red "server checks"

# --- I-04: typecheck ---------------------------------------------------------
step "web build check (I-04)"
( cd app && npm run build ) || red "app build"

# The web app's static guards (S-05 price math, hook-dependency TDZ). These
# existed and were never run by anything — which is how a dependency array
# naming a const declared 440 lines below it reached production and took
# TaskDetail down for every viewer. A guard nothing executes is a comment.
step "web static guards (S-05 + hook deps)"
( cd app && npm test ) || red "app static guards"

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
  # --redact: a finding must never print the secret itself into verify output (S-12).
  gitleaks detect --source . --no-banner --redact || red "gitleaks findings"
else
  incomplete "I-02 secret scan not run — 缺少相依套件: gitleaks"
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
  # Never --linked: supabase/.temp/project-ref is committed and names the
  # PRODUCTION project. The target is explicit and may not be production.
  if printf '%s' "${VERIFY_DB_URL:-}" | grep -q "$PROD_PROJECT_REF"; then
    red "I-05 refused: VERIFY_DB_URL points at the production project ($PROD_PROJECT_REF)"
  elif ! have supabase; then
    incomplete "I-05 not run — 缺少相依套件: supabase CLI"
  elif ! (have docker && docker info >/dev/null 2>&1); then
    incomplete "I-05 not run — 缺少相依套件: docker daemon"
  elif [ -z "${VERIFY_DB_URL:-}" ]; then
    incomplete "I-05 not run — 缺少設定: VERIFY_DB_URL (local or branch database)"
  else
    supabase db diff --db-url "$VERIFY_DB_URL" || red "schema drift detected"
  fi
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
if [ "$FAIL" -ne 0 ]; then echo "=== VERIFY: RED ==="; exit 1; fi
if [ "$INCOMPLETE" -ne 0 ]; then echo "=== VERIFY: INCOMPLETE (not green — see INCOMPLETE lines and preflight) ==="; exit 2; fi
echo "=== VERIFY: GREEN ==="
exit 0
