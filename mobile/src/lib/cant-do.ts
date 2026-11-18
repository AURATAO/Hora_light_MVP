/**
 * "I can't do this task": a supporter telling HO:RA they cannot do a task
 * they accepted (server/cant_do.go).
 *
 * Every word is the server's, from GET /config (server/cant_do.json): the
 * button, the reasons, the confirmation. There is NO built-in copy on
 * purpose. The reasons are codes the server validates, so a built-in list
 * could offer a reason the server no longer accepts; with nothing usable from
 * the server the button is simply not shown, and the supporter still has
 * "Contact HO:RA" in Profile.
 *
 * Pressing it changes nothing on the task. It tells HO:RA, and a person
 * decides what happens next.
 */

export interface CantDoReason {
  code: string;
  label: string;
}

export interface CantDoCopy {
  button: string;
  buttonCaption: string;
  heading: string;
  intro: string;
  reasons: CantDoReason[];
  boughtQuestion: string;
  boughtYes: string;
  boughtNo: string;
  notePlaceholder: string;
  submit: string;
  doneTitle: string;
  doneBody: string;
  reportedLabel: string;
}

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");

/** Whatever /config sent → the copy to show, or null when it cannot be used
 *  (an older backend, a broken file). Null means: no button. */
export function cantDoFromConfig(raw: unknown): CantDoCopy | null {
  const src = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
  const list = Array.isArray(src.reasons) ? src.reasons : [];
  const reasons: CantDoReason[] = [];
  const seen = new Set<string>();
  for (const entry of list) {
    const it = (entry && typeof entry === "object" ? entry : {}) as Record<string, unknown>;
    const code = str(it.code);
    const label = str(it.label);
    if (code && label && !seen.has(code)) {
      seen.add(code);
      reasons.push({ code, label });
    }
  }
  const copy: CantDoCopy = {
    button: str(src.button),
    buttonCaption: str(src.button_caption),
    heading: str(src.heading),
    intro: str(src.intro),
    reasons,
    boughtQuestion: str(src.bought_question),
    boughtYes: str(src.bought_yes),
    boughtNo: str(src.bought_no),
    notePlaceholder: str(src.note_placeholder),
    submit: str(src.submit),
    doneTitle: str(src.done_title),
    doneBody: str(src.done_body),
    reportedLabel: str(src.reported_label),
  };
  // The pieces the sheet cannot work without. The caption, intro and
  // placeholder are optional: the sheet reads fine without them.
  const required = [
    copy.button, copy.heading, copy.boughtQuestion, copy.boughtYes, copy.boughtNo,
    copy.submit, copy.doneTitle, copy.doneBody, copy.reportedLabel,
  ];
  if (reasons.length === 0 || required.some((v) => v === "")) return null;
  return copy;
}

export interface CantDoPayload {
  reason: string;
  bought_items: boolean;
  note?: string;
}

/** What to send, or the message to show instead. Both questions are required:
 *  "have you bought anything" is never assumed to be no. */
export function buildCantDoPayload(
  reason: string | null,
  bought: boolean | null,
  note: string,
): { payload: CantDoPayload } | { error: string } {
  if (!reason) return { error: "Choose what's going on." };
  if (bought === null) return { error: "Tell us whether you've bought anything." };
  const trimmed = note.trim();
  return { payload: { reason, bought_items: bought, ...(trimmed ? { note: trimmed } : {}) } };
}
