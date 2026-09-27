# App Store Connect — Review Notes (paste into "Notes" under App Review Information)

Fill in the two credentials from Render's `REVIEW_ACCOUNT_EMAIL` / `REVIEW_ACCOUNT_CODE` before pasting.

---

**Demo account**
Email: `<REVIEW_ACCOUNT_EMAIL>`
Code: `<REVIEW_ACCOUNT_CODE>` — enter it in the code field after tapping "Send code". Ignore any "could not send" notice; the code above always works for this account.

HO:RA is a two-sided marketplace for short, in-person tasks. Your demo account is set up as both a requester and an approved supporter, so you can walk both sides with one login. It has a saved test card already attached — you will never be asked for card details.

**Walkthrough**
1. Sign in: tick the Terms/Privacy checkbox, tap "Send code", enter the code above.
2. Home → "Post a task". Any title and any address (your own location is fine). Tap "Post task". A hold is placed on the test card.
3. Work tab → open the task you just posted → "Accept task". (Only this demo account can accept its own task.)
4. Tap "On my way". iOS asks for location permission here (see below). The requester view shows your live position.
5. Tap "Clock in" (timer starts), wait a moment, then "Clock out".
6. Tap "Complete task" → take or choose any photo as the completion/receipt photo → confirm. The card is charged for the time worked and an itemized receipt appears on the task.
7. Profile → Earnings shows the supporter payout for the same task.
8. From the task or the chat, the "⋯" menu offers Report user / Block user (Guideline 1.2).

**Three notes**
(a) HO:RA currently operates in New York City only. Your demo account is exempt from the service-area restriction on the server, so tasks can be posted and completed from any location.

(b) All payments for the demo account run in Stripe test mode regardless of the platform's mode. No real charge ever occurs. The saved card is Stripe's test Visa, 4242 4242 4242 4242 (any future expiry, any CVC), should you need to re-add it under Profile → Payment methods.

(c) Background location ("Always") is requested only when a supporter taps "On my way" (step 4). It shares the supporter's live position with the requester while travelling to and working on the task, so the requester knows when help is arriving. Sharing stops automatically when the supporter clocks out or the task ends; no location is collected at any other time.

Payments in HO:RA are for physical, real-world services performed in person and off-app (Guideline 3.1.3(e)), not for digital content or features, so In-App Purchase does not apply.
