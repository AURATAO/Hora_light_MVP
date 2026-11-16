/**
 * What a supporter confirms before accepting a task (PLAN 13c).
 *
 * The words, which task asks for what and how often are the BACKEND's
 * (server/accept_acks.json, served by GET /tasks/:id/accept-requirements).
 * This file only makes whatever arrives safe to render: an older backend, a
 * failed read or an odd payload all become "nothing to confirm", and the
 * accept goes ahead as it always did. When the server requires a
 * confirmation it refuses the accept itself (409 acknowledgment_required)
 * and sends the items again, so being lenient here cannot skip one.
 */

export interface AckItem {
  key: string;
  version: string;
  title: string;
  body: string;
}

export interface AcceptRequirements {
  heading: string;
  button: string;
  items: AckItem[];
}

export const ACK_FALLBACK_HEADING = "Before you accept";
export const ACK_FALLBACK_BUTTON = "I understand and accept";

function text(v: unknown): string {
  return typeof v === "string" ? v.trim() : "";
}

/** Any payload → requirements. Items without a key, a version or words are
 *  dropped: there is nothing to show and nothing to send back for them. */
export function normalizeAcceptRequirements(raw: unknown): AcceptRequirements {
  const src = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
  const list = Array.isArray(src.items) ? src.items : [];
  const items: AckItem[] = [];
  for (const entry of list) {
    const it = (entry && typeof entry === "object" ? entry : {}) as Record<string, unknown>;
    const item = { key: text(it.key), version: text(it.version), title: text(it.title), body: text(it.body) };
    if (item.key && item.version && (item.title || item.body)) items.push(item);
  }
  return {
    heading: text(src.heading) || ACK_FALLBACK_HEADING,
    button: text(src.button) || ACK_FALLBACK_BUTTON,
    items,
  };
}

/** What the accept sends back: the items shown, by key and version. */
export function acknowledgedPayload(items: AckItem[]): { key: string; version: string }[] {
  return items.map(({ key, version }) => ({ key, version }));
}

/** An item's body as paragraphs (the server separates them with a blank line). */
export function ackParagraphs(body: string): string[] {
  return body
    .split(/\n\s*\n/)
    .map((p) => p.replace(/\s*\n\s*/g, " ").trim())
    .filter(Boolean);
}
