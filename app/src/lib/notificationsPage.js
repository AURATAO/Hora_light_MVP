// Paging the notifications list: newest first, by `before=<created_at of the
// last row>` — the same cursor mobile uses (mobile/src/app/notifications.tsx).

/** Rows per request. */
export const NOTIFICATIONS_PAGE_SIZE = 20

/** `current` with `page` appended, minus anything already there: rows sharing
 *  the cursor's timestamp come back on both sides of the boundary. */
export function appendPage(current, page) {
  const seen = new Set(current.map((n) => n.id))
  return [...current, ...page.filter((n) => !seen.has(n.id))]
}

/** A full page means there may be more; a short one is the end. */
export function hasMoreAfter(page, pageSize = NOTIFICATIONS_PAGE_SIZE) {
  return Array.isArray(page) && page.length === pageSize
}

/** The cursor for the next request, or null with nothing loaded. */
export function nextCursor(items) {
  return items.length ? items[items.length - 1].created_at : null
}
