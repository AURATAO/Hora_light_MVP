// Shapes mirror the Go backend's JSON responses per BACKEND_REFERENCE.md.
// Nullability follows the documented Postgres column nullability, not guesses.

export type TaskCategory =
  | "task"
  | "companion"
  | "quick_errand"
  | "standard"
  | "half_day"
  | "full_day"
  | "delivery"
  | "grocery"
  | "laundry"
  | "queue"
  | "anything_else"
  | "companionship";

// "removed" is a platform takedown by the HO:RA team (admin_tasks.go), as
// opposed to "cancelled", which is the requester withdrawing their own task.
// Deliberately WITHOUT the backend's 'pending_payment'. That status means a
// task row exists only so a card hold can name it (Stripe Phase 2a) — it is
// filtered out of every list, feed and profile the server serves, and the
// post flow learns about it through a 402 carrying a task id rather than
// through a Task. Widening this union would push a state no screen may render
// into every status switch in the app.
export type TaskStatus = "open" | "completed" | "cancelled" | "removed";

export type SupporterStatus = "none" | "applied" | "approved" | "rejected";

/**
 * Which Post Task path produced a task — attribution only, never behaviour.
 * Matches tasks_created_via_check (supabase/migrations/20260827160000). Sent on
 * POST /tasks and stored write-once; the column is nullable, so tasks posted by
 * web or by a build older than this one are simply unattributed. Not read back:
 * no /tasks response carries it, and nothing in the app branches on it.
 */
export type TaskCreatedVia = "form" | "ai_parse" | "duplicate";

export type ValueRating = "not_worth" | "fair" | "great";

// Traction 3 questionnaire answers. Slugs only — the display labels live in
// TractionReviewSheet and are never sent or stored.
export type EaseRating = "very_easy" | "easy" | "neutral" | "difficult" | "very_difficult";
export type WouldUseAgain = "yes" | "maybe_task" | "maybe_cost" | "no";
export type RaterRole = "requester" | "supporter";

export type TaskRemovalReason =
  | "out_of_scope_private_residence"
  | "out_of_scope_other"
  | "inappropriate"
  | "other";

export type NotificationType =
  | "ORDER_ACCEPTED"
  | "CLOCK_IN"
  | "CLOCK_OUT"
  | "COMPLETED"
  | "COMPLETED_SUPPORTER"
  | "CANCELLED"
  | "TASK_REMOVED"
  | "TASK_REASSIGNED"
  | "NEW_MESSAGE"
  // Stripe Phase 2b. All five deep-link to the task screen, which is where
  // both the approve/deny card and the supporter's own request live — there is
  // no separate approval screen to route to.
  | "BUDGET_INCREASE_REQUESTED"
  | "TIME_EXTENSION_REQUESTED"
  | "EXTENSION_RESOLVED"
  | "TIME_CAP_WARNING"
  | "TIME_CAP_REACHED"
  // Stripe Phase 3. Deep-links to the task like the rest: the supporter's cut
  // is shown on the settlement card there, and Profile → Earnings is where the
  // running total lives.
  | "PAYOUT_SENT"
  // Live tracking. Fires once per task, the first time a pre-clock-in ping
  // lands within 100m of the address. Deep-links to the task screen, where the
  // live card is.
  | "SUPPORTER_ARRIVED";

// GET /auth/me — discriminated on `auth` so callers narrow before reading fields.
export type User =
  | {
      auth: true;
      id: string;
      email: string;
      /** Server-resolved (server/names.go): the chosen display name, or the
       *  email-prefix stand-in when none is set. Never derived on the client. */
      name: string;
      /** Whether `name` is theirs. False is what opens NamePromptSheet. */
      display_name_set: boolean;
      is_verified_supporter: boolean;
    }
  | { auth: false };

// GET /profile (own profile)
export interface Profile {
  email: string | null;
  name: string | null;
  phone: string | null;
  city: string | null;
  avatar_url: string | null;
  bio: string | null;
  beta_accepted: boolean;
  is_verified_supporter: boolean;
  // Both timestamps are `omitempty` on the Go side, so they arrive as
  // undefined (not null) when unset. Read supporter_status, never these —
  // the status derivation lives in Go only (S-05).
  supporter_applied_at?: string | null;
  supporter_rejected_at?: string | null;
  supporter_status: SupporterStatus;
  created_at: string;
  updated_at: string;
}

// GET /profiles/:id (public profile)
export interface PublicProfile {
  id: string;
  name: string | null;
  city: string | null;
  phone: string | null;
  avatar_url: string | null;
  bio: string | null;
  created_at: string;
  posted_total: number;
  posted_completed: number;
  asg_in_progress: number;
  asg_completed: number;
}

/**
 * The hold on the requester's card: what is reserved, on which card, and what
 * became of it.
 *
 * REQUESTER ONLY. The server attaches it to GET /tasks/:id and POST /tasks for
 * the requester and omits the key entirely for everyone else, so a supporter's
 * copy of a task does not carry it — this is optional here because it is
 * genuinely absent, not because it is sometimes empty.
 */
export interface TaskPayment {
  /** What is authorized on the card right now, or was before settlement. */
  authorized_cents: number;
  status: "requires_auth" | "authorized" | "captured" | "canceled" | "failed" | "capture_failed";
  /** Display only, and both absent on a hold placed before the card was
   *  recorded. Clients drop the card clause rather than inventing one. */
  card_brand?: string;
  card_last4?: string;
  /** What was actually taken, and the rest of the hold — which Stripe frees on
   *  its own. Releasing is NOT a refund and the copy must not call it one. */
  captured_cents: number;
  released_cents: number;
  /** What the hold is MADE OF. Present only when the two reconcile with
   *  authorized_cents — a breakdown that fails to add up to the number beside
   *  it is worse than no breakdown, so the server omits it rather than guess. */
  time_cost_cents?: number;
  shopping_budget_cents?: number;
}

/** What a requester owes from a completion that could not be charged. While
 *  this is non-null, POST /tasks answers 403 — see the banner. Supporters are
 *  never gated on anything here; the platform carries the float. */
export interface OutstandingBalance {
  total_cents: number;
  task_id: string;
  task_title: string;
  task_count: number;
}

export interface Task {
  id: string;
  title: string;
  description: string | null;
  category: TaskCategory;
  location_text: string | null;
  estimated_minutes: number | null;
  prepay_amount_cents: number | null;
  is_immediate: boolean | null;
  scheduled_at: string | null;
  requester_id: string | null;
  status: TaskStatus;
  assigned_to_id: string | null;
  transport_required: string | null;
  travel_time_minutes: number | null;
  total_estimate_minutes: number | null;
  completion_photo_url: string | null;
  completion_note: string | null;
  completed_at: string | null;
  /** FREE TEXT, written by whoever cancelled — the requester, or an ops
   *  admin. Never render it to the counterparty: use cancel_reason_label. */
  cancel_reason: string | null;
  cancelled_at: string | null;
  /** The preset's label behind cancel_reason, and the ONLY cancellation reason
   *  safe to show the other party. Detail-only, and absent when the task
   *  carries no preset — "Other", an ops cancel, or a row cancelled before
   *  codes existed. No reason beats somebody else's typing. */
  cancel_reason_label?: string | null;
  /** Takedown state. Only ever present on a removed task, and only on the
   * task-detail response — the admin's internal note is never sent. */
  removed_at?: string | null;
  removal_reason?: TaskRemovalReason | null;
  /** Whether the requester consented at post to the supporter running up to
   * 15 minutes past the estimate. Requester-only and detail-only: the server
   * selects it in GET /tasks/:id and omits it from every list. */
  auto_extend_consent?: boolean;
  /** What the receipt came to on a shopping task, and the photo behind it.
   * Written at completion (Stripe Phase 2b). Read the settlement object from
   * GET /tasks/:id/worklogs instead where you need the reimbursed figure —
   * this is the raw amount the supporter typed, before the budget cap. */
  receipt_amount_cents?: number | null;
  receipt_photo_url?: string | null;
  /** The hold on the card. Requester-only and detail-only — absent from every
   *  list response and from the supporter's copy of the same task. */
  payment?: TaskPayment;
  /** What cancelling this task right now would cost, and what would go back.
   *  Requester-only and detail-only, like `payment`, and present only while
   *  the task is open — a finished task cannot be cancelled. Every figure is
   *  the server's (S-05); the sheet renders them and derives nothing. */
  cancellation?: TaskCancellation;
  /** The approved shopping ceiling — the SAME column the server validates the
   * receipt against at completion, so it is what decides whether the completion
   * sheet shows the receipt step (see lib/task-budget.ts). Detail-only, and
   * sent to BOTH parties: the supporter needs it to know they owe a receipt,
   * the requester to see the ceiling their money is committed to. Absent (not
   * null) from list responses and to a browsing stranger. */
  shopping_budget_approved_cents?: number;
  /** When the assigned supporter tapped "On my way" — the pre-clock-in live
   * sharing window. Detail-only, and sent to BOTH parties: the supporter's
   * button reads it to know it has already been tapped, the requester's screen
   * reads it to know there is something live to watch. Absent (not null) when
   * they never tapped, which is the privacy default. */
  enroute_at?: string | null;
  created_at: string;
  /** Legacy email columns (S-60.1) — present on every /tasks response. Only
   * for TalkJS participant identity (matches web's existing id-by-email
   * scheme); never for permission checks — those use requester_id/assigned_to_id. */
  requester?: string;
  assigned_to?: string;
  /** What to PRINT for each party — resolved server-side. The emails above
   *  are for identity checks only and are never shown as a name. */
  requester_name?: string;
  assignee_name?: string;
}

export interface Worklog {
  id: string;
  task_id: string;
  /** Assignee email (S-60.2 legacy column — the API returns email here, not a UUID). */
  user: string;
  start_at: string;
  end_at: string | null;
  created_at: string;
  updated_at: string;
}

/**
 * One itemized quote. The same shape from POST /tasks/estimate (before any
 * work) and from GET /tasks/:id/worklogs (during and after) — every number on
 * it was computed in Go, and no client re-derives any of it (S-05).
 */
export interface TaskCost {
  base_fee_cents: number;
  included_minutes: number;
  per_minute_rate_cents: number;
  total_minutes: number;
  billable_minutes: number;
  time_cost_cents: number;
  shopping_budget_cents: number;
  total_cents: number;
  /** Settlement only (Stripe Phase 2b), absent on a pre-submission quote.
   * `billed_minutes` is `total_minutes` clamped to `cap_minutes`; when they
   * differ, the supporter worked longer than the requester agreed to pay for. */
  billed_minutes?: number;
  cap_minutes?: number;
  /** The verified receipt, reimbursed up to the approved budget + tolerance.
   * This is the number in `total_cents`; `shopping_budget_cents` is the
   * ceiling and was never a charge. */
  shopping_receipt_cents?: number;
  /** Whether per_minute_rate_cents is the evening rate, so a client can say
   *  WHY it is quoting more without knowing the rate or when it starts. */
  surge_rate?: boolean;
}

/**
 * The task's billable-time ceiling, itemized. The supporter is never told to
 * stop working at the cap — time past it simply is not billed.
 */
export interface TimeCap {
  estimate_minutes: number;
  auto_extend_minutes: number;
  approved_extra_minutes: number;
  /** Estimate + approved extensions: what the requester actually AGREED the
   *  job would take. Excludes auto-extend, which is a fuse nobody planned
   *  around, and is what the early warning is anchored to. */
  agreed_minutes: number;
  cap_minutes: number;
  warn_at_minutes: number;
  auto_extend_consent: boolean;
}

/**
 * What cancelling a task right now would do, priced by the server before the
 * requester commits to it.
 *
 * `within_grace` is the one case where `committed` is true and `charge_cents`
 * is zero: the free window that opens at acceptance so a mis-tap is an undo
 * rather than a $12 lesson. `grace_ends_at` is a DEADLINE, not a duration, so
 * a countdown ticking against it drifts by whatever the request took rather
 * than by however long the screen has been open.
 */
export interface TaskCancellation {
  committed: boolean;
  within_grace: boolean;
  grace_ends_at?: string | null;
  charge_cents: number;
  base_fee_cents: number;
  time_cost_cents: number;
  billed_minutes: number;
  release_cents: number;
  /** The reason presets, ordered. Server-owned for the same reason the budget
   *  ones are: the supporter's notification has to be able to name the option
   *  the requester picked. */
  reasons: { value: string; label: string }[];
}

export interface TimeCapState {
  cap: TimeCap;
  /** Includes the session currently running, unlike the billing total. */
  logged_minutes: number;
  reached: boolean;
  warning: boolean;
  remaining_minutes: number;
}

/**
 * What was (or will be) charged, and whether the money has actually moved.
 *
 * `state` is the one field to branch on:
 *   not_charged     no hold was ever placed — the figures are hypothetical
 *   estimated       still running; this is the bill so far
 *   captured        the money moved
 *   capture_failed  finished, and the charge did not go through. Ops have been
 *                   emailed; there is nothing for either party to do in-app.
 */
/**
 * One line of a finished task's decision trail: what was asked mid-task, what
 * was decided, and when.
 *
 * NOT an ExtensionRequest. That type carries an expiry countdown and a
 * supporter id — all of it about a live request somebody is waiting on, none
 * of it meaningful once the task is over.
 *
 * `status` is the raw value; `outcome` is the word to render ("No response"
 * for an expiry, because expiry is our mechanism and not their experience).
 * `fallback` is what actually ran when nobody answered, which is the useful
 * half of that sentence.
 */
export interface ExtensionRecord {
  id: string;
  kind: "budget" | "time";
  requested_cents?: number | null;
  requested_minutes?: number | null;
  reason_label?: string;
  status: "approved" | "denied" | "expired" | "pending";
  outcome: string;
  fallback?: string;
  requested_at: string;
  resolved_at?: string | null;
}

export interface Settlement {
  state: "not_charged" | "estimated" | "captured" | "capture_failed";
  approved_budget_cents: number;
  receipt_amount_cents: number;
  reimbursed_cents: number;
  logged_minutes: number;
  billed_minutes: number;
  time_cost_cents: number;
  total_cents: number;
  time_cap: TimeCapState;
  receipt_photo_url?: string;
  captured_cents?: number;
  settled_at?: string | null;
  /**
   * The mid-task asks and how they were decided, oldest first. Finished tasks
   * only, and absent entirely when nothing was ever asked — which is most
   * tasks, so the section is not rendered rather than rendered empty.
   *
   * While a task is RUNNING this is not the surface: GET /tasks/:id/extensions
   * is, and it carries the countdown and the fallback a supporter is actually
   * waiting on.
   */
  requests?: ExtensionRecord[];
  /**
   * What the SUPPORTER earned. Present ONLY on the supporter's own copy of
   * this payload — the server attaches it behind an assignment check, so a
   * requester's Settlement has no `earned` key at all.
   *
   * The mirror of the requester-only `payment` block on Task: one settlement,
   * two disjoint views, and neither ever reaches the other party.
   */
  earned?: SupporterEarnings;
}

/** A settled task's money, from the side of the person who did the work. */
export interface SupporterEarnings {
  /** Base fee + billable minutes. What they made. */
  time_cents: number;
  /** Money they fronted, coming back. Separate on purpose — it is not income. */
  reimbursement_cents: number;
  /** The sum, net of the platform cut (zero during beta). */
  total_cents: number;
  /** Absent when no transfer exists yet: the figures above are what is OWED. */
  payout_status?: "pending" | "paid" | "failed";
}

// GET /tasks/:id/worklogs
export interface WorklogsSummary {
  worklogs: Worklog[];
  total_minutes: number;
  total_cost_cents: number;
  cost: TaskCost | null;
  settlement: Settlement | null;
}

/** A supporter's mid-task ask, and the requester's answer (Stripe Phase 2b). */
export type ExtensionKind = "budget" | "time";
export type ExtensionStatus = "pending" | "approved" | "denied" | "expired";
export type ExtensionFallback = "buy_alternative" | "skip_item";

export interface ExtensionRequest {
  id: string;
  task_id: string;
  supporter_id: string;
  kind: ExtensionKind;
  /** Set on a budget request. The ADDITIONAL amount asked for, not a new total. */
  requested_cents?: number;
  /** Set on a time request. The ADDITIONAL minutes asked for. */
  requested_minutes?: number;
  /** What is STORED: a preset slug ("price_higher"), "other: <text>", or free
   *  text from a request written before the presets existed. */
  reason?: string;
  /** What is READ: the human sentence for whichever of those it is. Render
   *  this, never `reason` — a requester approving a charge must not be shown
   *  "item_unavailable". */
  reason_label?: string;
  fallback?: ExtensionFallback;
  fallback_note?: string;
  status: ExtensionStatus;
  created_at: string;
  resolved_at?: string | null;
  /** When silence becomes a denial. Server-computed, so a countdown runs
   * against the server's clock rather than the phone's. */
  expires_at: string;
  /** The supporter's own fallback read back to them, once this was denied or
   * expired. Empty on a time request, which has no fallback. */
  fallback_instruction?: string;
}

// GET /tasks/:id/extensions
export interface ExtensionsResponse {
  items: ExtensionRequest[];
  approved_budget_cents: number;
  time_cap: TimeCap;
  /** The minute amounts a requester can grant with one tap. */
  time_choices: number[];
  /** The ordered budget-reason presets. Server-owned, like time_choices — a
   *  product vocabulary in two hardcoded client copies drifts the first time a
   *  fifth option is added. "Other" is not in here: it is a mode the form
   *  enters, which then stores "other: <text>". */
  budget_reasons?: { value: string; label: string }[];
  timeout_minutes: number;
  tolerance_cents: number;
}

export interface AppNotification {
  id: string;
  user_id: string;
  task_id: string | null;
  type: NotificationType;
  title: string;
  body: string;
  unread: boolean;
  via_email: boolean;
  email_sent_at: string | null;
  created_at: string;
}

// Two producers, two shapes — server/main.go's Review struct (POST
// /tasks/:id/reviews) has no `omitempty` on value_rating/comment, so they're
// always-present strings ("" when unset), never JSON null; reviewer_id/
// supporter_id are only on that struct, not on GET /profiles/:id/reviews'
// anonymous ReviewItem, which instead adds task_title. would_rehire is a Go
// *bool either way, so it's the one field that's genuinely null-able.
export interface Review {
  id: string;
  task_id: string;
  /** Only on POST /tasks/:id/reviews responses. */
  reviewer_id?: string;
  /** Only on POST /tasks/:id/reviews responses. */
  supporter_id?: string;
  /** Only on GET /profiles/:id/reviews responses. */
  task_title?: string;
  /**
   * Null on a supporter-submitted questionnaire, which rates nobody. Rows
   * reaching a supporter's public profile always have it (the server filters
   * `stars is not null`), but the type is shared with POST responses.
   */
  stars: number | null;
  /** "" when unset — coalesced/zero-valued server-side, never null. */
  value_rating: ValueRating | "";
  would_rehire: boolean | null;
  /** "" when unset — coalesced/zero-valued server-side, never null. */
  comment: string;
  /** Traction 3 fields — on POST responses only, "" when unset. */
  rater_role?: RaterRole;
  ease_rating?: EaseRating | "";
  would_use_again?: WouldUseAgain | "";
  /**
   * Product feedback for the HO:RA team, never public: it is not selected by
   * GET /profiles/:id/reviews and must not be rendered on a supporter profile.
   */
  open_feedback?: string;
  created_at: string;
}

// POST /ai/parse-task response
export interface ParsedTask {
  title: string;
  category: TaskCategory;
  description: string;
  location_1: string;
  location_2: string;
  duration_minutes: number;
  scheduled: boolean;
  scheduled_time: string;
}

export interface GpsPing {
  id: string;
  task_id: string;
  user_id: string;
  lat: number;
  lng: number;
  accuracy: number | null;
  created_at: string;
}

// GET /tasks/:id/gps-latest response — the supporter's most recent ping, or
// null when none has been recorded yet. Note the endpoint returns `200 null`
// (not 404) for the no-ping case, so the mobile read side treats null as the
// waiting state rather than an error.
export interface LatestLocation {
  lat: number;
  lng: number;
  accuracy: number | null;
  created_at: string;
}

/**
 * GET /tasks/:id/live — the requester's view of a supporter on their way.
 *
 * REQUESTER-ONLY: the server 404s this for the supporter and for everyone
 * else, so there is no branch here for a partial payload.
 *
 * `state` is derived on the server from `distance_m`, and the client never
 * re-derives it (S-05, and the same reason every cost figure comes down the
 * wire finished). `distance_m` is in metres and is NEVER rendered verbatim —
 * formatDistance in ./live-tracking turns it into "0.8 mi away".
 */
export type LiveState = "on_the_way" | "almost_there" | "at_door" | "working" | "unavailable";

export interface LiveLocation {
  state: LiveState;
  /** The supporter's last known position. Null until their first ping. */
  lat: number | null;
  lng: number | null;
  updated_at: string | null;
  /** Metres to the task address. Null when the position is stale (the server
   * withholds it rather than making a claim about the present) or when the
   * task has no coordinates. */
  distance_m: number | null;
  supporter: { name: string; avatar_url: string };
  /** Location A — the requester's own address, sent so one poll draws the
   * whole map. Null on a task posted without coordinates. */
  destination: { lat: number; lng: number } | null;
  enroute_at: string | null;
  clocked_in: boolean;
}

// POST /tasks/:id/estimate-travel response
export interface TravelEstimate {
  travel_minutes: number;
  task_minutes: number;
  total_minutes: number;
  display: string;
}
