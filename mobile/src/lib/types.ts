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
  | "NEW_MESSAGE";

// GET /auth/me — discriminated on `auth` so callers narrow before reading fields.
export type User =
  | {
      auth: true;
      id: string;
      email: string;
      name: string;
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
  cancel_reason: string | null;
  cancelled_at: string | null;
  /** Takedown state. Only ever present on a removed task, and only on the
   * task-detail response — the admin's internal note is never sent. */
  removed_at?: string | null;
  removal_reason?: TaskRemovalReason | null;
  created_at: string;
  /** Legacy email columns (S-60.1) — present on every /tasks response. Only
   * for TalkJS participant identity (matches web's existing id-by-email
   * scheme); never for permission checks — those use requester_id/assigned_to_id. */
  requester?: string;
  assigned_to?: string;
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

// GET /tasks/:id/worklogs
export interface WorklogsSummary {
  worklogs: Worklog[];
  total_minutes: number;
  total_cost_cents: number;
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

// POST /tasks/:id/estimate-travel response
export interface TravelEstimate {
  travel_minutes: number;
  task_minutes: number;
  total_minutes: number;
  display: string;
}
