# D-28: warmer English copy — proposals for review

Generated 2026-11-11 by `go run ./cmd/agenttest -voice-copy` (anthropic:claude-sonnet-5-5, voice.md v0.5). **Not applied to copy.go.**

Every proposal passed the runtime rule check (`CheckVoice`): placeholders, prices, times, YES/STOP/HELP lines, links, quoted titles and names unchanged; no new number, link, question or promise; at most 10% longer. 167 templates: **42 proposed**, 53 the model kept as they are, 20 proposals refused by the check (listed, with the refused text), 34 with nothing to reword, 18 excluded.

Format: `CopyName`: "before" → "after". `\n` is a line break. The machine-readable list is in the `.json` next to this file.

## Greeting and how HO:RA works

- `CopyAbout`: "HO:RA sends a vetted local supporter to do the errand for you. You see the estimate before anything is booked, and you pay for the time actually used.\n\nTell me what you need and when, and I'll price it." → "HO:RA sends a vetted local supporter to run the errand for you. You see the estimate before anything is booked, and you pay for the time actually used.\n\nTell me what you need and when, and I'll price it."

## Service area and addresses

- `CopyAskWhatWhere`: "Sure — what do you need done, and where should the supporter go? (When, too, if it isn't as soon as possible.)" → "Happy to help — what do you need done, and where should the supporter go? (When, too, if it isn't as soon as possible.)"
- `CopyAskWhere`: "Where should the supporter go for “%s”? An address or a place name is enough." → "Where should the supporter go for “%s”? An address or a place name works."

## Waiting on the customer

- `CopyStillWaitingApproval`: "Still waiting on your answer to your supporter’s request." → "Your supporter is still waiting on your answer to their request."
- `CopyStillWaitingName`: "Still waiting on your first name." → "I'm still waiting on your first name."

## Supporter requests (approve / decline / items)

- `CopyItemAskTimed`: "No answer within %d minutes: your supporter will decide." → "No answer within %d minutes, so your supporter will decide."
- `CopyItemChoseAlternative`: "Got it: %s will get %s instead." → "Got it — %s will get %s instead."
- `CopyItemChoseSkip`: "Got it: %s will skip %s." → "Got it, %s will skip %s."
- `CopyItemExpired`: "That question already timed out, so your supporter is deciding using what you asked for." → "That question already timed out, so your supporter will go by what you originally asked for."
- `CopyApprovalTryAgain`: "Sorry, I couldn't take that answer just now. Your supporter's request is still open: please send it again in a moment." → "Sorry, I couldn't take that answer just now. Your supporter's request is still open, so please send it again in a moment."
- `CopyApprovalOnItsWay`: "Your supporter has a question for you — it's on its way. If it hasn't arrived in a few minutes, text me again." → "Your supporter has a question for you, and it's on its way. If it hasn't arrived in a few minutes, text me again."
- `CopyApprovalExpired`: "That request already timed out and was declined automatically." → "That request timed out, so it was declined automatically."
- `CopyApprovalGone`: "I couldn't find that request anymore — nothing was changed." → "I couldn't find that request anymore, so nothing was changed."

## Cancelling

- `CopyCancelNone`: "You have no open tasks to cancel right now." → "You don't have any open tasks to cancel right now."
- `CopyCancelWhich`: "Which task do you want to cancel?%s\nReply with the number." → "Which task would you like to cancel?%s\nReply with the number."
- `CopyCancelGone`: "“%s” can't be cancelled any more — it's already finished or closed." → "“%s” can't be cancelled anymore — it's already finished or closed."
- `CopyCancelTakenOver`: "Your supporter's request came in first, so “%s” was not cancelled. Answer it above, then send “cancel task” if you still want to." → "Your supporter's request came in first, so “%s” wasn't cancelled. Answer it above, then send “cancel task” if you still want to."

## Where is my supporter

- `CopyWhereWorking`: "%s is clocked in on “%s” — %d min so far." → "%s is clocked in on “%s”, %d min so far."
- `CopyWhereSetOff`: "%s has just set off — no position yet, give it a minute." → "%s has just set off. No position yet, so give it a minute."

## Talking to a HO:RA teammate

- `CopyHandoffUpdateFailed`: "I couldn't pass that to the HO:RA team just now. Please send it again in a few minutes, or email info@my-hora.com." → "Sorry, I couldn't pass that to the HO:RA team just now. Please send it again in a few minutes, or email info@my-hora.com."
- `CopyHandoffTimedOut`: "Sorry — no one from the HO:RA team has replied in the last 10 minutes, so I'm back to help. The team has your messages; if it's about a payment or a problem, email info@my-hora.com and we'll follow up. You can also say “real person” again." → "Sorry for the wait — no one from the HO:RA team has replied in the last 10 minutes, so I'm back to help. The team has your messages; if it's about a payment or a problem, email info@my-hora.com and we'll follow up. You can also say “real person” again."

## Passing messages to the supporter

- `CopyWhichTaskBadPick`: "That number isn't on the list. Which one did you mean?" → "Hmm, that number isn't on the list. Which one did you mean?"
- `CopyRelayedDraftDropped`: "Passed that on to %s. The new errand is dropped." → "Passed that on to %s. I've dropped the new errand."
- `CopyAskedToHurry`: "I've asked %s to speed up a little." → "I've asked %s to pick up the pace a little."
- `CopyAmbiguousYes`: "I can't tell which message that answers. To answer my question, send just your answer on its own; to answer your supporter, reply to their message." → "I'm not sure which message that's answering. To answer my question, send just your answer on its own; to answer your supporter, reply to their message."
- `CopyHoldDropped`: "OK, I won't send that." → "No problem, I won't send that."
- `CopyBackToHold`: "Your earlier message is still waiting — which task is it for?" → "Your earlier message is still waiting. Which task is it for?"

## Drafting an errand (name, budget, extra time)

- `CopyAskBudget`: "Will your supporter be buying something for this? About how much should I set aside, e.g. $20? Reply "nothing" if there's nothing to buy." → "Will your supporter be buying anything for this? About how much should I set aside, e.g. $20? Reply "nothing" if there's nothing to buy."
- `CopyDraftWhileActive`: "Booking this as a new errand. If it was for your supporter instead, reply %s." → "I'll book this as a new errand. If it was for your supporter instead, reply %s."
- `CopyExtraTimeAck`: "Got it: up to %d extra minutes are OK, and nothing else changes. Reply YES to book, NO to drop it, or tell me what to change." → "Got it — up to %d extra minutes are fine, and nothing else changes. Reply YES to book, NO to drop it, or tell me what to change."
- `CopyTooManyErrands`: "That's more than I can book in one go (up to %d at a time). Send me the first few and we'll take it from there." → "That's more than I can book at once (up to %d at a time). Send me the first few and we'll take it from there."

## Booking and payment

- `CopyOneStepToBookAgain`: "Here's a fresh link to finish booking:\n\n%s" → "Here's a fresh link to finish your booking:\n\n%s"
- `CopyBooked`: "Booked. I'm finding a supporter now and will text you when someone accepts. To cancel, reply "cancel task"." → "You're booked. I'm finding a supporter now and will text you when someone accepts. To cancel, reply "cancel task"."
- `CopyBookedAll`: "Booked all %d. I'm finding supporters now and will text you as each one is accepted. To cancel, reply "cancel task"." → "All %d are booked. I'm finding supporters now and will text you as each one is accepted. To cancel, reply "cancel task"."
- `CopyPostFailed`: "Something went wrong on our side while booking. Reply YES to try again." → "Sorry, something went wrong on our side while booking. Reply YES to try again."
- `CopyPriceChanged`: "The price has gone up since that summary, here's the updated one." → "The price has gone up since that summary, so here's the updated one."
- `CopyTimePassed`: "“%s” was set for %s, and that time has passed. What time should I book it for instead?" → "“%s” was set for %s, but that time has passed. What time should I book it for instead?"
- `CopyPostingStuck`: "Something went wrong booking that. Reply YES to try again." → "Sorry, something went wrong booking that. Reply YES to try again."
- `CopyBookedHandedOff`: "Your booking went through. A HO:RA teammate is with you for the rest." → "Your booking went through. A HO:RA teammate will take it from here."
- `CopyPostNotDoneHandedOff`: "That booking didn't go through. A HO:RA teammate is with you and can help finish it." → "That booking didn't go through. A HO:RA teammate is here with you and can help you finish it."

## Photos and receipts

- `CopyPhotoTooLarge`: "That photo is too large for me (10 MB max). Could you send a smaller one or a screenshot?" → "That photo's a bit too large for me (10 MB max). Could you send a smaller one or a screenshot?"
- `CopyReceiptPhotoNotSent`: "The receipt photo couldn't be sent here." → "Sorry, the receipt photo couldn't be sent here."

## Refused by the rule check (kept as they are)

- `CopyApprovalFirst` (question_added): "Please answer your supporter’s request first, and then tell me about the new errand." — refused: "Could you answer your supporter's request first? Then tell me about the new errand."
- `CopyReceipt` (too_long): "Receipt: %s." — refused: "Here's your receipt: %s."
- `CopyReceiptNoPhoto` (too_long): "Receipt: %s. (The photo couldn't be sent here.)" — refused: "Here's your receipt: %s. (The photo couldn't be sent here.)"
- `CopyStillWaitingBudget` (too_long): "Still waiting on the shopping budget — an amount, or “nothing”." — refused: "Still waiting on your shopping budget — just send an amount, or “nothing”."
- `CopyStillWaitingGeneric` (too_long): "Still waiting on your answer above." — refused: "Still waiting on your answer above whenever you're ready."
- `CopyWhichSupporterMidQuestion` (name_dropped): "Which supporter is that for? Start with their name — %s." — refused: "Which supporter is that for? Just start with their name — %s."
- `CopyHoldGone` (too_long): "Your supporter has finished, so I couldn't pass that on. Tell me if you need something new." — refused: "Your supporter has already finished, so I couldn't pass that on. Just tell me if you need something new."
- `CopyRelayGone` (too_long): "That task has finished, so I can't pass this on. Tell me if you need something new." — refused: "That task is already finished, so I can't pass this on. Just tell me if you need something new."
- `CopySummaryStale` (too_long): "That summary is a bit old, here's the updated one." — refused: "That summary was a little out of date, so here's the updated one."
- `CopyPostingLost` (question_added): "Something went wrong booking that. Please send me the errand again." — refused: "Sorry, something went wrong booking that. Could you send me the errand again?"
- `CopyCancelNothing` (too_long): "OK, nothing cancelled." — refused: "No problem, nothing's been cancelled."
- `CopyCancelWhyReask` (too_long): "Just the number, please — why cancel “%s”?%s\nOr NO to keep it." — refused: "Just the number, please — what's the reason for cancelling “%s”?%s\nOr NO to keep it."
- `CopyWhereWorkingHere` (too_long): "%s is at the task's address, clocked in on “%s” — %d min so far." — refused: "%s has arrived at the task's address and clocked in on “%s” — %d min so far."
- `CopyWhereWorkingStale` (too_long): "%s is clocked in on “%s” (%d min so far); no fresh location from their phone%s." — refused: "%s is clocked in on “%s” (%d min so far), but there's no fresh location from their phone%s."
- `CopyCancelConfirmFree` (name_dropped): "Cancel “%s”%s? It's free to cancel now. Reply YES to cancel, or NO to keep it." — refused: "Want to cancel “%s”%s? It's free to cancel now. Reply YES to cancel, or NO to keep it."
- `CopyCancelFailed` (too_long): "I couldn't cancel “%s” just now. You can cancel it in the app, or try again in a minute." — refused: "I wasn't able to cancel “%s” just now. You can cancel it in the app, or I can try again in a minute."
- `CopyApprovalAskTimed` (too_long): "Reply YES to approve or NO to decline. No answer within %d minutes counts as a no." — refused: "Reply YES to approve or NO to decline. If I don't hear back within %d minutes, I'll take that as a no."
- `CopyApproved` (too_long): "Approved: %s. Your supporter can go ahead." — refused: "All set: %s is approved. Your supporter can go ahead."
- `CopyDeclined` (too_long): "Declined: %s. Your supporter has been told." — refused: "That didn't go through: %s. Your supporter has been told."
- `CopyApprovalAlready` (too_long): "That request was already %s." — refused: "Looks like that request was already %s."

## Kept by the model (already reads well)

- `CopyWelcome`: "Hi! I'm your HO:RA concierge. I connect you with real people who run your errands: groceries, laundry, pickups, waiting in line, and more.\n\nWe only operate in New York City's five boroughs: Manhattan, Brooklyn, Queens, the Bronx and Staten Island.\n\nWhat can I help you with today?"
- `CopyOutOfArea`: "Sorry, HO:RA only runs errands in New York City's five boroughs right now. If you have an errand in NYC, send it over!"
- `CopyAskClearerAddress`: "I couldn't find "%s" exactly. Could you send the full address with the borough or ZIP code? (We run errands in all five NYC boroughs.)"
- `CopyWhichBorough`: "Which borough is %s in?"
- `CopyAreaCheckDown`: "I can't check that address right now, so I can't book it yet. Please send it again in a few minutes."
- `CopyHelloAgain`: "Hi again! It's your HO:RA concierge. What can I help you with today?"
- `CopyAck`: "Got it! I'm here whenever you need a hand."
- `CopyAckShort`: "Got it."
- `CopyOnMyWay`: "%s is on the way for %s. I'll text you when they arrive."
- `CopyTextOnly`: "I can only read text for now. Tell me what you need and when."
- `CopyAskName`: "Got it: %s.\n\nBefore I price it, what's your first name?"
- `CopyAskBudgetFor`: "For “%s”: will your supporter be buying something? About how much should I set aside, e.g. $20? Reply "nothing" if there's nothing to buy."
- `CopyWhichTask`: "Which task is that about?"
- `CopyWhichTaskNothingHeld`: "Got it. What would you like to tell them?"
- `CopyRelayFailed`: "I couldn't pass that on to your supporter just now. Please try again in a moment."
- `CopyRelayFailedPick`: "I couldn't pass that on to your supporter just now. Reply the number again in a moment."
- `CopyPhotoAttached`: "Got the photo — your supporter will see it."
- `CopyPhotoWhatFor`: "Got your photo. What's it for? Tell me the errand and I'll attach it for your supporter."
- `CopyPhotoNotImage`: "I can only take photos here (JPEG, PNG or HEIC), not other files. Could you send it as a photo or a screenshot?"
- `CopyPhotoFailed`: "I couldn't open that photo. Could you send it again?"
- `CopyReceiptPhotoOnly`: "The receipt:"
- `CopyPassedThanks`: "I passed your thanks to %s."
- `CopyLetKnow`: "I've let %s know."
- `CopyAskNameAgain`: "Just your first name is fine."
- `CopyOneStepToBook`: "One step to book, about a minute: confirm your email and add a card. Nothing is charged until the task is done.\n\n%s\n\nI'll book it as soon as you finish."
- `CopySetupAlreadySent`: "I just sent you a link to finish booking. It works for 30 minutes. Lost it? Text me again in a couple of minutes for a new one, or reply NO to drop it."
- `CopyDropped`: "No worries at all, I won't book that. I'm here whenever you need me."
- `CopyBookingInFlight`: "One moment, I'm booking it now."
- `Copy3DSHandoff`: "Your bank wants to confirm this payment. A HO:RA teammate will text you shortly to finish it."
- `Copy3DSHandoffNoTeam`: "Your bank wants to confirm this payment, and I couldn't reach the HO:RA team just now. Please email info@my-hora.com and we'll finish it with you."
- `CopyHandoffWhileBooking`: "Let me get a HO:RA teammate for you. They'll text you here shortly. The booking you confirmed is still going through, and I'll tell you how it went."
- `CopyHandoffHolding`: "A HO:RA teammate will get back to you shortly."
- `CopyCancelWhy`: "Before I cancel “%s” — what's the reason?%s\nReply with the number (or NO to keep the task)."
- `CopyWhereWorkingAt`: "%s is working on “%s” (%d min so far)%s%s."
- `CopyWhereAtDoor`: "%s is at the door."
- `CopyWhereAlmost`: "%s is almost there%s%s."
- `CopyWhereOnTheWay`: "%s is on the way%s%s."
- `CopyWhereUnavailableLine`: "%s's phone hasn't reported a location for a bit%s"
- `CopyCancelConfirmFee`: "Cancel “%s”%s? Your supporter has already accepted it, so cancelling now costs %s (the %s base fee plus any time already worked). Reply YES to cancel, or NO to keep it."
- `CopyCancelledFree`: "All set: “%s” is cancelled. No charge. No worries at all, I'm here whenever you need me."
- `CopyCancelledFee`: "All set: “%s” is cancelled. The cancellation charge is %s. I'm here whenever you need me."
- `CopyCancelKept`: "Sounds good, “%s” stays booked."
- `CopyCancelConfirmUpTo`: "Cancel “%s”%s? Your supporter is working on it right now, so cancelling costs up to %s (the %s base fee plus the time worked so far, capped at the agreed time). The exact amount is set the moment you confirm. Reply YES to cancel, or NO to keep it."
- `CopyCancelConfirmClosing`: "Cancel “%s”%s? The free-cancel window is about to end, so cancelling will cost %s (the %s base fee plus any time already worked). Reply YES to cancel, or NO to keep it."
- `CopyCancelInApp`: "Your supporter is on “%s” right now, so the cancellation charge is still changing. Please cancel it in the app, where you'll see the exact amount first."
- `CopyCancelledUnknown`: "All set: “%s” is cancelled. Any cancellation charge will show in the app. I'm here whenever you need me."
- `CopyDroppedCancelHint`: "No worries at all, I won't book that. If you meant a task that's already posted, say “cancel task”."
- `CopyErrandAfterApproval`: "Got it — I'll set up “%s” right after you answer this request."
- `CopyHandoffFailed`: "I couldn't reach the HO:RA team just now. Please try again in a few minutes, or email info@my-hora.com."
- `CopyHandoffStart`: "Let me get a HO:RA teammate for you. They'll text you here shortly — you can keep writing in the meantime."
- `CopyHandoffReleased`: "You're back with your HO:RA concierge. What can I help you with?"
- `CopyApprovalNote`: "%s's note: %s"
- `CopyBackToDraft`: "Back to your new request:"

## Not proposed

- `CopyParseFailed` — matched by exact text (lastReplyWasParseFailure)
- `CopyAskBudgetAgain` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyAutoExtendOn` — a line of the booking summary (word for word)
- `CopyAutoExtendOff` — a line of the booking summary (word for word)
- `CopyWhichTaskHint` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyRelayWhileConfirm` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyRelayWhileApproval` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyRelayWhileConfirmIM` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyRelayWhileApprovalIM` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyRelayWhileBudgetIM` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyDraftStillPending` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyBudgetStillPending` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyApprovalStillPending` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyConfirmReask` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyNoteForSupporter` — a line of the booking summary (word for word)
- `CopyPhotoLine` — a line of the booking summary (word for word)
- `CopyPhotosLine` — a line of the booking summary (word for word)
- `CopyPassedOn` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyToSupporterHurry` — sent to the supporter, not the customer
- `CopyToSupporterThanks` — sent to the supporter, not the customer
- `CopyToSupporterLetKnow` — sent to the supporter, not the customer
- `CopyStillWaitingBooking` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyStillWaitingSetup` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyRelayWhileBudget` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyNeedCard` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyBookedSome` — a fragment glued into another sentence
- `CopyCardProblem` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyRetryYes` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyWhereMap` — a fragment glued into another sentence
- `CopyWhereNotYetLine` — a fragment glued into another sentence
- `CopyWhereAsked` — a fragment glued into another sentence
- `CopyWhereAskedAlready` — a fragment glued into another sentence
- `CopyWhereAskByName` — a fragment glued into another sentence
- `CopyCancelReasonEcho` — a fragment glued into another sentence
- `CopyCancelReask` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyCancelSetAside` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyCancelAfterApproval` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyCancelMoreInApp` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyCancelRetry` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyStillWaitingCancel` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyStillWaitingCancelReason` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyCancelNotDone` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyStillWaitingCancelChoice` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyApprovalAsk` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyApprovalReask` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyItemAsk` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyItemReask` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyNotPassedOn` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyCouldntPassOn` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyApprovalTimedOut` — every word is fixed (YES/STOP lines, prices, links, values)
- `CopyCancelHint` — "cancel task" wording
- `CopyOptInWelcome` — opt-in reply (TCPA)
