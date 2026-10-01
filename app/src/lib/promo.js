// Promo codes on the post form. The server decides everything about a code
// (S-05); this file only reads what it said.

/**
 * The sentence for a refused code, or null when the error was something else.
 * Every refusal the server sends — invalid, expired, already used, not your
 * first task, fully redeemed — arrives as 400 `{error: "promo_*", message}`,
 * and this relays the sentence rather than paraphrasing. Mirrors mobile's
 * readPromoFailure (mobile/src/lib/api.ts).
 */
export function readPromoFailure(err) {
  if (!err || err.status !== 400) return null
  const body = err.body && typeof err.body === 'object' ? err.body : {}
  if (typeof body.error !== 'string' || !body.error.startsWith('promo_')) return null
  return typeof body.message === 'string' && body.message
    ? body.message
    : "That promo code can't be used. Post without one."
}

/** The total a money surface shows: the discounted one when the server applied
 *  a promo and said what it comes to, the plain one otherwise. Takes a quote
 *  (POST /tasks/estimate) or a settlement — both carry the same two keys. */
export function totalAfterPromo(source, totalCents) {
  const discount = source?.promo_discount_cents || 0
  return discount > 0 && source.total_after_promo_cents !== undefined
    ? source.total_after_promo_cents
    : totalCents
}
