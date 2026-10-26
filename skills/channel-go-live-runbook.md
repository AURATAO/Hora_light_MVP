# Go-live runbook — merging the messaging channel to `main`

Written 2026-10-26, for the merge of `feat/messaging-channel`. Everything the
merge needs, in the order it has to happen, with the reason for each step and
what to do when one fails. Read phase 0 first: it is the reason this can be
done calmly.

Owner: Aura. Claude can do every DB and code step; the secrets and the
dashboards are hers.

---

## Phase 0 — what the merge does and does not do

**Merging the code changes nothing in production.** Every channel route is
registered only when `CHANNEL_WEBHOOKS_ENABLED=1`
(`server/internal/channel/config.go`, `Enabled()`), and that variable does not
exist on production. Without it:

- no webhook endpoints answer,
- no outbound message can be sent (the Sendblue adapter is unconfigured and
  fails closed — `ErrNotConfigured`),
- the setup page is served but nothing links to it,
- the existing app and web flows are untouched.

So the merge and the switch-on are two separate decisions, and the second one
is a single environment variable. **Do not do both in the same sitting.** Merge,
watch production behave exactly as before, then switch on.

The one exception is the schema: the channel tables have to exist before the
flag goes on (phase 3), and adding them changes nothing by itself.

---

## Phase 1 — merge conditions

| # | Condition | State 2026-10-26 |
|---|---|---|
| C-1 | I-05 schema check green on a machine with Docker + supabase CLI | **done** — "No schema changes found", CLI v2.122.0, 2026-10-26 |
| C-2 | gitleaks green on a full clone | **done** — v8.21.2, 425 commits, no leaks |
| C-3 | App Store review freeze over | **done** — approved 2026-10-24 |
| C-4 | Delete `server/channel_setup.html` + `.js` (S-02 exception expires at go-live) | **do during phase 5** |

### C-1, how it was run (kept for the next time)

On the Mac, with Docker Desktop running:

```bash
brew install supabase/tap/supabase     # or: brew upgrade supabase
supabase --version                     # record this in the merge note
cd <repo> && git checkout feat/messaging-channel
supabase db diff --db-url "<STAGING BRANCH connection string>" --schema public
```

`--schema public` matters: without it the diff also reports the `storage`,
`auth` and `realtime` schemas, which Supabase upgrades on its own and which our
migrations never contain — pages of noise with any real finding buried in it.

Empty output = green. Use the **staging branch** (`qxohqvkmzxokdyzrysgr`), never
`--linked` — `supabase/.temp/project-ref` names production, and `verify.sh`
refuses a URL containing it (D-16 R-5).

**A "missing version" is not drift.** Production's ledger holds 33 versions
against 41 files here, and several differ in timestamp for identical content
because they were applied through the MCP tool, which stamps at apply time.
Only `db diff` answers this question.

### Also run, from the repo root

```bash
./verify.sh            # expect: only I-05 pending, nothing red
cd server && go test ./... -race     # green since 2026-10-26
```

---

## Phase 2 — the merge

```bash
git checkout main && git pull
git merge --no-ff feat/messaging-channel
# conflicts expected in: none known (main has not moved since 2026-10-18)
git push
```

Render redeploys production from `main` automatically. **Watch the startup log
and confirm it says what it said before:**

```
[payments] Stripe initialized in LIVE mode
[payments] enabled: true (test mode: false) | webhook secret set: true
[payments] secrets set — connect webhook: true | v2 webhook: … | sandbox key: true | …
```

There should be **no** `[channel]` lines and no `/webhooks/channel/*` routes in
the route dump. If there are, `CHANNEL_WEBHOOKS_ENABLED` is set on production
by accident — unset it and redeploy before going further.

Then use the app normally for a few minutes: post a task, accept it, clock in.
Nothing about those paths changed, and that is the point of checking.

---

## Phase 3 — production schema

Six channel migrations, in this order, plus one data migration that is already
applied:

```
20261019120000_messaging_channel.sql              tables: identities, messages,
                                                  conversations, setup_tokens
20261020120000_channel_messages_address_e164.sql
20261022120000_channel_approval_columns.sql
20261022130000_channel_approval_question_pid.sql
20261024120000_channel_cancel_by_chat.sql
20261024210000_channel_handoff_timeout.sql
20261026150000_revoke_client_grants_users_…sql  three REVOKEs; a no-op on a
                                                database that already holds no
                                                client grants, which production
                                                does — it exists so a REBUILT
                                                database holds none either
```

`20261026093000_stripe_live_mode_reset_test_identifiers.sql` is **already
applied and recorded** — it must never run again (it would clear every real
customer). Its ledger row exists for exactly that reason.

Apply them, then run the four CLAUDE.md Rule 3 queries against production and
confirm **3 / 0 / 0 / 0**. The new tables hold phone numbers and message text:
they ship RLS-enabled with zero policies and no client grants, and that is
what those queries prove. A different answer stops the go-live.

---

## Phase 4 — production environment

Set these on the production Render service **before** the flag in phase 5.
Nothing here is in the repo; Aura sets them in the dashboard.

| Variable | Value | Why |
|---|---|---|
| `SENDBLUE_API_KEY` | from Sendblue | iMessage send |
| `SENDBLUE_API_SECRET` | from Sendblue | " |
| `SENDBLUE_FROM_NUMBER` | **the dedicated line**, E.164 | the sandbox line is shared and capped at 10 contacts |
| `SENDBLUE_WEBHOOK_SECRET` | from Sendblue | inbound webhooks are rejected without it |
| `LLM_API_KEY` | the production Baseten key | the agent and the classifier |
| `CHANNEL_OPS_EMAILS` | ops allowlist | hand-off notices |
| `CHANNEL_OPS_NUMBERS` | Aura's number, E.164 | hand-off SMS |
| `CHANNEL_AGENT` | `1` | the requester agent (on in staging since 2026-10-25) |
| `CHANNEL_SETUP_WEB_URL` | `https://<webapp host>/channel/setup` | points texted links at the React page; unset = the embedded Go page |
| `APP_ENV` | `production` | already set |
| `POSTMARK_API_TOKEN` | already set | — |

Check at the same time:

- **`CORS_ALLOW_ORIGINS` contains the webapp origin.** If it does not, every
  texted setup link opens and every visitor sees "we couldn't reach HO:RA",
  with nothing in the server log. One real link proves it.
- **`STRIPE_INCREMENTAL_AUTH` is absent.** On an account without flexible
  payments enabled, every post 402s.

Not set, deliberately: `WHATSAPP_*`. WhatsApp is not part of this go-live (it
needs a permanent token, a production number, and a rotated verify token).
iMessage first.

---

## Phase 5 — switch on

In this order:

1. **Sendblue dashboard:** point the receive webhook at
   `https://<production API>/webhooks/channel/sendblue`.
2. **Render:** `CHANNEL_WEBHOOKS_ENABLED=1`. The service redeploys.
3. Confirm the startup log now lists the channel routes, and that
   `CHANNEL_SETUP_WEB_URL` is the webapp (phase 6 checks it end to end).
4. **C-4:** delete `server/channel_setup.html`, `server/channel_setup.js`, the
   `/channel/setup.js` route and the redirect branch in `channel_setup.go`;
   commit. The S-02/S-30 exception for those two files expires here. Do it
   after step 3 proves the webapp page works, not before.

---

## Phase 6 — verify, with a real first booking

From a phone whose number has never texted the production line (this is the
only way to see the setup page — a linked number never gets another link):

1. Text the production number: *"I need someone to pick up a coffee"*.
2. Expect a quote, reply **YES**.
3. Expect a setup link. Open it: it must be the **webapp** page (Tailwind, the
   HO:RA wordmark at the top), not the old Go page.
4. Email → 6-digit code → card (a real card; expect a 3DS challenge on an EEA
   card) → "Booked".
5. In the ops web app: **Ops → Chats** shows the conversation; the task appears
   in the task feed.
6. In the logs: `[channel] inbound`, `[payments] pre-auth … status=requires_capture`.
7. Accept it from the supporter side and cancel it free within the grace
   window, or let it run to completion if someone is actually available.

Then the three things only a real conversation exercises, all on the test
checklist (artifact): cancel by chat, hand off to a person, "where is he?".

---

## Phase 7 — if something is wrong

**Switch off:** unset `CHANNEL_WEBHOOKS_ENABLED` (or set it to `0`) and
redeploy. Takes about a minute. That stops every inbound webhook and every
outbound message immediately.

What it leaves behind: conversations mid-flow stay in whatever state they were
in, and nobody is told anything — a person who was waiting for a reply gets
silence. For anything beyond a few minutes, text the people in
`channel_identities` by hand, or turn it back on, resolve each chat from
**Ops → Chats**, and then switch off.

**Do not** roll back the schema to switch off. The tables are inert without
the flag, and dropping them loses the conversations.

**Money is not part of the rollback.** A task booked by chat is a task like any
other: its hold, capture and payout follow the normal paths, and a completed
task settles whether the channel is on or off (`payments_resettle.go` sweeps a
settlement whose chain died).

---

## Known not included in this go-live

- **WhatsApp** — permanent token, production number, rotated verify token.
- **Mobile** — the `time_cap_reached` message on clock-in and laundry's "Ask
  for a budget" at $0. Both wait for the next App Store submission.
- **Dispatch automation** (PLAN §13b) — deferred 2026-10-26; the ops alert
  threshold is settled at **N = 5 minutes** for when it is built.
- **A full CSP on the webapp** — the other response headers shipped
  2026-10-26; the CSP needs every third-party origin enumerated and every page
  clicked through afterwards.
- **Agent model baseline** — the 110-case eval has not been run against the
  production model with a real key.
