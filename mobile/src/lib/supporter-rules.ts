/**
 * The supporter rules (Profile → Supporter rules, and the application form).
 *
 * The SERVER's list wins when GET /config carries one (server/
 * supporter_rules.json), so the wording can change after a legal review
 * without an app release. BUILT_IN_RULES is the same list, shown when the
 * server sends none (an older backend, no connection).
 */

export interface SupporterRule {
  /** hand | message | door | shield | footprints | ban; anything else shows a neutral icon. */
  icon: string;
  title: string;
  body: string;
}

export const BUILT_IN_RULES: SupporterRule[] = [
  {
    icon: "hand",
    title: "You can always say no",
    body: "Decline any task, or leave one, if you feel unsafe. There's no penalty, and you don't have to explain.",
  },
  {
    icon: "message",
    title: "Can't finish? Tell us right away",
    body: "If you can't start or finish a task you accepted, tell HO:RA straight away so we can find someone else: tap \"I can't do this task\" on the task, or message us. How the time you logged is handled depends on what happened, so tell us and we'll work it out with you. If you feel unsafe, leave first and tell us after.",
  },
  {
    icon: "door",
    title: "Homes only for a specific errand",
    body: "Go inside a home only for the errand you accepted, with the customer or an adult they named present, or with clear access instructions from them.",
  },
  {
    icon: "shield",
    title: "Never alone with minors",
    body: "If you would be left alone with someone under 18, stop and leave the task.",
  },
  {
    icon: "footprints",
    // No rides, ever: a supporter never carries a person. They may use
    // their own vehicle to carry ITEMS when the task says so.
    title: "Never drive a customer",
    body: "Never give anyone a ride. You can use your own car to carry items when the task says so.",
  },
  {
    icon: "ban",
    title: "No alcohol, tobacco, drugs or prescriptions",
    body: "Never buy, carry or deliver alcohol, tobacco, vapes, cannabis or illegal drugs, even if the customer asks. At a pharmacy, over-the-counter items only: never pick up a prescription.",
  },
];

/** Whatever /config sent → rules to show. Rules without a title or a body
 *  are dropped; nothing usable means the built-in list. */
export function rulesFromConfig(raw: unknown): SupporterRule[] {
  const src = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
  const list = Array.isArray(src.items) ? src.items : [];
  const out: SupporterRule[] = [];
  for (const entry of list) {
    const it = (entry && typeof entry === "object" ? entry : {}) as Record<string, unknown>;
    const title = typeof it.title === "string" ? it.title.trim() : "";
    const body = typeof it.body === "string" ? it.body.trim() : "";
    if (title && body) out.push({ icon: typeof it.icon === "string" ? it.icon : "", title, body });
  }
  return out.length > 0 ? out : BUILT_IN_RULES;
}
