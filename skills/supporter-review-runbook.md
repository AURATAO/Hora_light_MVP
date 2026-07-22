# Runbook: reviewing supporter applications

**Audience:** anyone on the ops admin allowlist.
**Background:** D-08 (rejected status, approve/reject endpoints).

A user applies from web (`/become-supporter`) or mobile (Work tab → Apply now).
That sets `profiles.supporter_applied_at` and emails the admins. Nothing else
happens automatically — approval and rejection are human decisions made here.

`supporter_status` is derived in Go from three columns and is never stored
(S-05): `is_verified_supporter` → `approved`; else `supporter_rejected_at` →
`rejected`; else `supporter_applied_at` → `applied`; else `none`.

---

## Primary path — the ops panel

1. Sign in with an allowlisted admin account and open **`/ops`**.
2. Select the **Supporter applications** tab.
3. **Pending applications** lists everyone awaiting a decision, newest first,
   with name, email, phone, city and the date they applied.
4. Run whatever off-platform checks the current policy requires (US location,
   valid US government-issued ID — the eligibility copy both clients show).
5. Click **Approve** or **Reject**. Each asks for confirmation, then the row
   updates immediately and the list refetches from the server. If the call
   fails, a toast explains why and the row returns to pending — a decision you
   see stick is a decision that landed.
6. **Decided** (collapsed, below) is the history: every approved/rejected
   applicant with their status and rejection date. Expand it to check what was
   decided before, or to confirm a decision you just made.

What the buttons do:
- **Approve** → `is_verified_supporter = true`, clears `supporter_rejected_at`.
  The user can accept tasks immediately (their next Work-tab load shows the
  feed).
- **Reject** → `supporter_rejected_at = now()`, `is_verified_supporter = false`.
  Mobile shows "Application not approved" with a Contact support action.

**Re-applying:** a rejected user can apply again from the app; that clears
`supporter_rejected_at` and they reappear in Pending. No ops action needed to
give someone a second chance.

**Applicant notifications are still manual.** Neither endpoint emails or
notifies the applicant (deliberately deferred — D-08). Whatever you decide,
tell them yourself. Rejected users are pointed at `info@my-hora.com`, so watch
that inbox after a rejection batch.

---

## Fallback / API reference — curl

Use when the panel is unreachable (Render deploy in flight, web build broken)
or when scripting a batch. All three require an allowlisted admin identity;
`$TOKEN` is a Supabase access token for that account, `$API` is the backend
base URL (production: the Render service URL, e.g. `https://core.horaapp.co`).

```bash
# List applications (pending + decided), newest first
curl -s "$API/ops/supporter-applications" \
  -H "Authorization: Bearer $TOKEN" | jq '.[] | {name, email, city, supporter_status}'

# Approve — by email, or swap in {"profile_id":"<uuid>"}
curl -s -X POST "$API/ops/supporter-approve" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"email":"applicant@example.com"}'

# Reject
curl -s -X POST "$API/ops/supporter-reject" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"email":"applicant@example.com"}'
```

Responses: `{"ok":true}` on success; `404 {"error":"profile not found"}` if no
profile matched (check for a typo in the email before assuming the row is
missing); `403 {"error":"not authorized"}` if the account is not on the
allowlist.

**Never do this with direct SQL against the production database.** The columns
look simple enough to `UPDATE` by hand, but the endpoints keep approve/reject
symmetric (approving clears any prior rejection) and keep the derivation
honest. Dashboard SQL on production is out per S-21 regardless.

---

## Adding a new admin

The allowlist is hardcoded in `server/main.go` (`opsAdmins`) and mirrored as a
display gate in `app/src/pages/OpsFeed.jsx`. Adding someone is a code change,
a deploy, **and** a Tier 3 decision record (S-14). Both lists need the address
or the person sees the page and gets 403s from every call.
