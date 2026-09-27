# D-15: The App Review account is a sealed sandbox on Stripe test keys; Report and Block ship server-enforced

**Date:** 2026-09-27
**Status:** Accepted.
**Trigger:** App Store submission. Apple's reviewer must walk both sides of a task from one login, from outside NYC, without a real charge — and Guideline 1.2 requires report/block for an app with chat. Tier 3 — auth (the review login), a migration, payment routing, and a new authorization surface.

## Decision

1. **`users.is_sandbox` marks the App Review account**, set by the review-login seed (`review_account.go`) and by nothing else — on the row, not derived from `REVIEW_ACCOUNT_EMAIL`, because the Stripe objects it owns are test-mode objects for ever, whether or not the env vars are still set after approval.

2. **A sandbox user's money is never real.** Every Stripe call made on behalf of a user or a task now names its secret key explicitly (`stripeKeyForUser` / `stripeKeyForTask` / `stripeKeyForAccount`, `sandbox.go`); the package-level `stripe.Key` is read through those and nowhere else. A sandbox user resolves to `STRIPE_SANDBOX_SECRET_KEY`, or to the platform key only while that is itself a test key (today's posture, so nothing has to be configured before the October switch). On a live platform with no sandbox key the call is **refused** (`errSandboxKeyUnavailable`), never downgraded to live. A live key placed in the sandbox slot is refused too. `PAYMENTS_ENFORCED` is treated as on for a sandbox user (`paymentsEnforcedFor`), so the reviewer walks hold → capture → payout, not a payments-off shortcut. The review login attaches Stripe's `pm_card_visa` test token as the account's default card, so no card is ever typed.

3. **A sandbox user never meets a real user.** The Available feed, task read, accept, and admin reassign all test `sameSandboxPartition`: a sandbox task is invisible to and un-acceptable by every real account, and the reverse. Inside the partition the sandbox account — and only it — may accept its own task (`can_self_accept`), which is what lets one login play requester and supporter.

4. **There is no NYC service-area gate on the server, and none is added.** The clients only bias address autocomplete toward New York. A reviewer in California posts and accepts inside the sandbox partition, which is what keeps that task off real supporters' boards — the "exemption" is the partition.

5. **Report and Block are task-scoped and server-authorized** (`safety.go`): the target must be the other party on that task — the session uid decides who "me" is (S-11). A report writes `user_reports`, an `audit_logs` row (`USER_REPORTED`) and an email to the ops allowlist; reasons are a closed set the server serves (`GET /safety/report-reasons`), like cancel reasons. A block is stored directionally in `user_blocks` and enforced **mutually** everywhere a match could form (feed, accept, reassign, open-task read), sets both TalkJS participants to `Read` access, makes the webhook stop pushing between the pair, and tells ops when a task is live between them. Ops reads reports at `GET /admin/reports` (web ops panel → User reports) and resolves them with an audit row.

6. **Terms acceptance is recorded.** Both clients already gated sign-in on a consent checkbox; nothing stored the answer. `PATCH /profile {terms_accepted: true}` stamps `profiles.terms_accepted_at` once (first acceptance kept); the post-task form links the same two documents beside the button.

## Constitution impact
- Standards added: none.
- Standards modified/retired: none. S-01's table list gains `user_reports`, `user_blocks` (CLAUDE.md Rule 1 updated); both ship RLS-enabled, zero policies, client grants revoked, in the same migration (S-13).
- Invariants: the four Rule 3 queries still return three rows, none, none, none.

## Context and alternatives

**Why per-call keys and not a second process.** A second Render service on test keys would need its own DB, its own webhook endpoints and its own build — for one account. Naming the key per call is ~20 call sites and a test that asserts the sandbox hold uses the sandbox key.

**Why refuse rather than fall back.** A reviewer who cannot pay is a failed review; a reviewer who pays real money is an incident. The only safe default on a live platform with no test key is to stop.

**Why the partition instead of a geofence exemption.** A geofence exemption would let a California task reach real NYC supporters' boards. The partition answers the same need (the reviewer can post from anywhere) without any real account ever seeing a review task.

**Why blocks are mutual.** Apple's requirement is that blocked users cannot contact each other. Directional storage keeps the audit trail honest about who acted; mutual enforcement is what the person who blocked actually wanted.

**Why the chat is read-only rather than hidden.** A blocked person who was mid-task still needs the history (what was agreed, where to be); what they must not be able to do is send. TalkJS's `Read` access does exactly that.
