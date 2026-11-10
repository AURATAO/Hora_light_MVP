<!-- Paste into App Store Connect → App Review Information → Notes. Replace the two <…> placeholders with Render's REVIEW_ACCOUNT_EMAIL / REVIEW_ACCOUNT_CODE first; never commit the live values.
     The pair committed in bc0096b is dead: REVIEW_ACCOUNT_CODE was rotated on Render on 2026-09-27; verified 2026-09-27T20:28Z, POST /auth/review-login with the old code answers 401. -->
Demo account
Email: <REVIEW_ACCOUNT_EMAIL>
Code: <REVIEW_ACCOUNT_CODE> — tap "Send code", then enter the code above. (An emailed code may also arrive; ignore it — the fixed code above always works.)

HO:RA is a service for short, in-person tasks in New York City (errands, pickups, waiting in line, a hand with something). This app is for supporters only: the verified people who do the tasks. It is where they find tasks, do them and get paid.

How requesters use HO:RA: people who need help do not use this app. They text HO:RA over iMessage or WhatsApp, or book on the web at horaapp.co. They describe the errand in their own words, confirm the price and pay by card on the web. The task then appears in this app for supporters to accept. An account that has not applied to be a supporter sees an "Apply now" card and a link to horaapp.co for booking.

Your demo account is already an approved supporter, and a few demo tasks are waiting on its Tasks tab every time you sign in. The demo tasks are posted by the demo account itself, so you may also receive the requester-side notifications for them on the same device.

Walkthrough
1. Sign in: tick the Terms/Privacy checkbox, tap "Send code", enter the code above.
2. On first opening the Tasks tab, the app explains why it wants notifications ("Turn on notifications so you don't miss jobs near you") before iOS asks. Either answer is fine.
   Tasks tab → open any demo task → "Accept task". (If the tab is empty, wait a few seconds and pull down to refresh — new demo tasks are added automatically.)
3. Tap "On my way". iOS asks for location permission here (see note c).
4. Tap "Clock in" (timer starts), wait a moment, then "Clock out".
5. Tap "Complete task" → choose any photo from the library (or take one) as the completion photo → confirm. An itemized summary appears.
6. Earnings tab shows the payout for the same task. My jobs lists active and finished tasks.
7. Profile → Supporter rules shows the safety rules for supporters (also shown on the application form before applying): decline any task if you feel unsafe; enter a home only for a specific errand with the customer or a named adult present, or with clear access instructions; never alone with minors; companionship only in public places.
8. From the task or the chat, the "⋯" menu offers Report user / Block user (Guideline 1.2).
9. Account deletion is available in-app under Profile (Guideline 5.1.1(v)).

Three notes
(a) HO:RA currently operates in New York City only. The demo tasks carry New York addresses, but your demo account can accept and complete them from any location.

(b) All payments for the demo account run in Stripe test mode regardless of the platform's mode. No real charge or payout ever occurs. Supporters are paid out through Stripe; the app never takes payment from the person using it.

(c) Background location ("Always") is requested only when a supporter taps "On my way" (step 3). It shares the supporter's live position with the requester while travelling to and working on the task. Sharing stops automatically when the supporter clocks out or the task ends; no location is collected at any other time.

The app sells nothing. Supporters are paid for physical, real-world services performed in person (Guideline 3.1.3(e)), so In-App Purchase does not apply.