# D-21: Email addresses still appear in server logs

**Date:** 2026-10-30
**Status:** Proposed. This records the problem for later; nothing has been decided yet.
**Trigger:** While checking staging on 2026-10-30, the `[createTask] body: %+v` line was found printing the requester's title, description and address. That line is fixed in `726790c`. A sweep for similar lines found many more that print email addresses. The rule being broken is "No PII in logs", one of the standing constraints.

## The open problem
`grep -n 'log.Printf.*\(email\|Email\)' server/*.go server/internal/*/*.go` (excluding tests) gives about 39 hits in 15 files. This is a grep, not an audit. Some hits only mention a flag such as `sendEmail=%v`, and each one needs to be triaged. The hits:

- `internal/notify/notify.go` (8): `[email] SEND FAILED to=`, postmark errors `to=… body=`, `[notify] … emailTo=`.
- `main.go` (12), including:
  - `[listAvailableTasks] … email=`
  - `[notify][rich] … email=`
  - lines around 1169–1286 and 5257–5305
- `talkjs_admin.go` (3): reassign `add/remove/moved %s`, which prints the old and new emails.
- `review_account.go` (3): review-login `email=`. This is `REVIEW_ACCOUNT_*`, which is left untouched by standing ruling. Decide it separately, or leave it out.
- `promo.go` (2), `channel_admin.go` (2).
- One each in:
  - `admin_reassign.go`, `channel_booker.go`, `live_tracking.go` (`[arrival] supporter=`), `names.go`
  - `payments_connect.go`, `payments_payouts.go`, `payments_resettle.go`, `payments_settlement.go` (ops emails `to=`), `stripe_webhook.go`

## Options for later (the Arbiter's call)
1. **Print the user id instead of the email.** The id is already in most of these lines. Where there is no user, as with ops recipients or a postmark failure, mask the address: `a***@example.com`, in the same style as `channel.PhoneHint` for phone numbers.
2. **Add one helper, `emailHint(s)`, and use it everywhere.** Lines stay readable for debugging, and full addresses never reach Render's log retention.
3. **Leave the ops/admin addresses as they are.** These are HO:RA's own staff addresses, not users'. Fix only lines that print a requester's or supporter's address.

Recommendation: options 2 and 3 together. One helper and one review. Rows where the address is the provider's own error body, such as postmark `body=`, need a look, because the body can echo the recipient back.

## Evidence
- Staging log 2026-10-30 13:07:59, `[createTask] body: {Title:… Description:… LocationText:350 5th Ave, New York …}`. Fixed in `726790c`.
- The grep above.
