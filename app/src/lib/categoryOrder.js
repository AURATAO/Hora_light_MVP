/**
 * THE ORDER, in one place. The category picker on the home page and every
 * label the post form prints read this.
 *
 * It used to be defined separately here and in mobile, in different orders,
 * and the mobile picker carried "Anything else" while its home page did not —
 * so the same person saw the categories one way on the way in and another
 * once they got there (build 11). This is the web copy of mobile's
 * `POST_CATEGORY_ORDER` (mobile/src/lib/categories.ts), and
 * categoryOrder.test.mjs pins the two to each other byte for byte.
 *
 * "companionship" is the display value; NewTask submits it as "companion".
 */
export const POST_CATEGORY_ORDER = Object.freeze([
  'quick_errand',
  'delivery',
  'laundry',
  'grocery',
  'queue',
  'companionship',
])

/** How each category introduces itself on the picker. Copy only — no order. */
export const CATEGORY_META = Object.freeze({
  quick_errand:  { title: 'Quick Errand',       subtitle: 'Pickups, drop-offs, small jobs' },
  delivery:      { title: 'Same-day Delivery',  subtitle: 'Packages, pickups, drop-offs' },
  laundry:       { title: 'Laundry Service',    subtitle: 'Wash, fold, dry cleaning' },
  grocery:       { title: 'Grocery & Errands',  subtitle: 'Shopping, pharmacy, supplies' },
  queue:         { title: 'Queue & Wait',       subtitle: 'Lines, reservations, waiting' },
  companionship: { title: 'Companionship',      subtitle: 'Appointments, walks, company' },
})
