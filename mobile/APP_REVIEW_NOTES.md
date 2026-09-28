<!-- Paste into App Store Connect → App Review Information → Notes. Replace the two <…> placeholders with Render's REVIEW_ACCOUNT_EMAIL / REVIEW_ACCOUNT_CODE first; never commit the live values.
     The pair committed in bc0096b is dead: REVIEW_ACCOUNT_CODE was rotated on Render on 2026-09-27; verified 2026-09-27T20:28Z, POST /auth/review-login with the old code answers 401. -->
Demo account
Email: <REVIEW_ACCOUNT_EMAIL>
Code: <REVIEW_ACCOUNT_CODE> — tap "Send code", then enter the code above. (An emailed code may also arrive; ignore it — the fixed code above always works.)

HO:RA is a two-sided marketplace for short, in-person tasks. Your demo account is set up as both a requester and an approved supporter, so you can walk both sides with one login. It has a saved test card already attached — you will never be asked for card details. Because the demo account is both parties, you will receive both sides' notifications (requester and supporter) on the same device.

Walkthrough
1. Sign in: tick the Terms/Privacy checkbox, tap "Send code", enter the code above.
2. Home → tap the "Groceries? A queue? Company?" banner (or any category). Describe the task in a sentence, tap "Continue", pick any address suggestion. Tap "Post task". A hold is placed on the test card (the reserved amount is shown on screen).
3. Earn tab → open the task you just posted → "Accept task".
4. Tap "On my way". iOS asks for location permission here (see note c). The requester view shows your live position.
5. Tap "Clock in" (timer starts), wait a moment, then "Clock out".
6. Tap "Complete task" → choose any photo from the library (or take one) as the completion photo → confirm. The test card is charged for the time worked; an itemized receipt appears.
7. Profile → Earnings shows the supporter payout for the same task.
8. From the task or the chat, the "⋯" menu offers Report user / Block user (Guideline 1.2).
9. Account deletion is available in-app under Profile (Guideline 5.1.1(v)).

Three notes
(a) HO:RA currently operates in New York City only. Your demo account is exempt from the service-area restriction on the server, so tasks can be posted and completed from any location.

(b) All payments for the demo account run in Stripe test mode regardless of the platform's mode. No real charge ever occurs. The saved card is Stripe's test Visa, 4242 4242 4242 4242 (any future expiry, any CVC), should you need to re-add it under Profile → Payment methods.

(c) Background location ("Always") is requested only when a supporter taps "On my way" (step 4). It shares the supporter's live position with the requester while travelling to and working on the task. Sharing stops automatically when the supporter clocks out or the task ends; no location is collected at any other time.

Payments in HO:RA are for physical, real-world services performed in person and off-app (Guideline 3.1.3(e)), not for digital content or features, so In-App Purchase does not apply.