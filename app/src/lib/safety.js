// Report / Block copy and fallbacks (App Store Guideline 1.2). Mirrors
// mobile/src/lib/safety.ts — the reason presets come from the server
// (server/safety.go); this fallback only covers a failed fetch.

export const FALLBACK_REPORT_REASONS = [
  { value: 'harassment', label: 'Harassment or threats' },
  { value: 'inappropriate_messages', label: 'Inappropriate or offensive messages' },
  { value: 'unsafe_behavior', label: 'Unsafe behavior' },
  { value: 'no_show', label: "Didn't show up" },
  { value: 'scam_or_payment', label: 'Scam or off-app payment request' },
  { value: 'other', label: 'Something else' },
]

export function safetyTargetName(name) {
  const n = (name ?? '').trim()
  return n || 'this person'
}

export function blockConfirmTitle(name) {
  return `Block ${safetyTargetName(name)}?`
}

export const BLOCK_CONFIRM_BODY =
  "You won't be matched with each other on any task again, and this chat becomes read-only for both of you. The HO:RA team is notified."

export const CHAT_BLOCKED_LINE =
  'This conversation is read-only because one of you blocked the other. You can still see earlier messages. Need help? Report the user from the menu above.'

export const REPORT_SENT_BODY =
  "Thanks for telling us. The HO:RA team reviews every report within 24 hours. If you're in danger, call 911."
