import { ApiError } from "./api-error";
import type {
  AppNotification,
  EaseRating,
  ExtensionFallback,
  ExtensionRequest,
  ExtensionsResponse,
  GpsPing,
  LatestLocation,
  LiveLocation,
  OutstandingBalance,
  ParsedTask,
  Profile,
  PublicProfile,
  Review,
  Settlement,
  Task,
  TaskCategory,
  TaskCost,
  TaskPayment,
  TaskCreatedVia,
  TravelEstimate,
  User,
  ValueRating,
  Worklog,
  WorklogsSummary,
  WouldUseAgain,
} from "./types";

export const API_BASE_URL = "https://core.horaapp.co";

type ApiFetchOptions = Omit<RequestInit, "body"> & { body?: unknown };

// All Hora business data (tasks, profiles, notifications, ...) goes through
// the Go backend, never a direct Supabase table read (S-01). This is the one
// fetch wrapper client code should use for that traffic.
export async function apiFetch<T>(path: string, options: ApiFetchOptions = {}): Promise<T> {
  const { body, headers, ...rest } = options;
  const isFormData = body instanceof FormData;

  const response = await fetch(`${API_BASE_URL}${path}`, {
    ...rest,
    credentials: "include",
    headers: {
      ...(isFormData ? {} : { "Content-Type": "application/json" }),
      ...headers,
    },
    body: isFormData ? body : body !== undefined ? JSON.stringify(body) : undefined,
  });

  const text = await response.text();
  const data = text ? JSON.parse(text) : null;

  if (!response.ok) {
    throw new ApiError(response.status, data);
  }

  return data as T;
}

// Accepts the various narrow *Params interfaces below; none declare an index
// signature, so entries are read through a cast rather than widening the
// parameter type (which would drop excess-property checking at call sites).
function toQueryString(params?: object): string {
  if (!params) return "";
  const search = new URLSearchParams();
  const entries = Object.entries(params) as [string, string | number | boolean | undefined][];
  for (const [key, value] of entries) {
    if (value !== undefined) search.set(key, String(value));
  }
  const query = search.toString();
  return query ? `?${query}` : "";
}

// A picked/captured local file, as expo-image-picker's asset shape gives it
// to us: `fileName` is frequently null on iOS, `mimeType` is usually present
// but not guaranteed.
export interface FilePart {
  uri: string;
  mimeType?: string | null;
  fileName?: string | null;
}

// React Native's global `fetch` is NOT React Native's classic fetch as of
// this Expo SDK — `expo/winter` installs its own WinterCG-compliant fetch
// over `globalThis.fetch` at startup (see node_modules/expo/src/winter/
// runtime.native.ts; opt out via EXPO_PUBLIC_USE_RN_FETCH=1, which we don't
// do repo-wide). Its multipart encoder (winter/fetch/convertFormData.ts)
// only accepts a string, a real `Blob`, or an object with a `.bytes()`
// method — RN's classic proprietary shorthand part, `{ uri, name, type }`,
// matches none of those and hits its final `else` branch verbatim:
// `throw new Error('Unsupported FormDataPart implementation')`. That
// shorthand used to work because RN's own fetch/FormData understood it
// directly; it silently stopped applying once Expo's fetch became the
// global one.
//
// The fix is to hand it a real Blob. RN's Blob constructor only accepts
// `Array<Blob | string>` (not ArrayBuffer), so we can't build one from raw
// bytes directly — instead we read the local file via XMLHttpRequest
// (unaffected by the winter runtime, which only patches `fetch`/`FormData`/
// `AbortSignal`, not `XMLHttpRequest`) with `responseType: "blob"`, which is
// RN's long-standing supported way to turn a local `file://` URI into a
// Blob. A Blob's `.type` has no setter, so to guarantee our own
// extension/mimeType-derived content type (rather than whatever the raw
// file read happened to infer) we wrap it in `new Blob([rawBlob], { type })`.
function uriToBlob(uri: string): Promise<Blob> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.onload = () => resolve(xhr.response);
    xhr.onerror = () => reject(new Error("Couldn't read the selected file."));
    xhr.responseType = "blob";
    xhr.open("GET", uri, true);
    xhr.send(null);
  });
}

function inferMimeType(filename: string): string {
  const ext = /\.(\w+)$/.exec(filename)?.[1]?.toLowerCase();
  return ext === "png" ? "image/png" : ext === "heic" ? "image/heic" : "image/jpeg";
}

async function buildFileFormData(file: FilePart, fieldName: string): Promise<FormData> {
  const filename = file.fileName || file.uri.split("/").pop() || `upload-${Date.now()}`;
  const type = file.mimeType || inferMimeType(filename);

  const rawBlob = await uriToBlob(file.uri);
  const blob = new Blob([rawBlob], { type });

  const formData = new FormData();
  formData.append(fieldName, blob, filename);
  return formData;
}

export interface KeysetParams {
  before_created_at?: string;
  before_id?: string;
  limit?: number;
}

/** A keyset cursor as the server hands it back, for "load more". */
export interface KeysetCursor {
  before_created_at: string;
  before_id: string;
}

/**
 * A page of a list, plus the two things a preview needs that a bare array
 * cannot carry: where the next page starts, and how many rows exist in total.
 *
 * `total` is the whole history, not the page — it is what lets a screen
 * showing three rows say "See all (47)". Zero from a server that could not
 * count, which the clients render as no link at all rather than "See all (0)".
 */
export interface TaskPage {
  items: Task[];
  next: KeysetCursor | null;
  total: number;
}

// Every keyset-paginated task list endpoint (/tasks/posted, /posted/closed,
// /available, /assigned, /done) wraps its rows in { items, next }, not a bare
// array — and a handler that ever built `items` from a nil slice would
// serialize it as JSON `null`. Route every caller through this so a
// missing/null list can never reach a screen's .map().
interface KeysetEnvelope<T> {
  items: T[] | null;
  next?: unknown;
  total?: number;
}

function unwrapItems<T>(envelope: KeysetEnvelope<T> | null | undefined): T[] {
  return Array.isArray(envelope?.items) ? envelope.items : [];
}

/** The same envelope, kept whole — for the callers that need `next`/`total`. */
function unwrapPage(envelope: KeysetEnvelope<Task> | null | undefined): TaskPage {
  const next = envelope?.next as KeysetCursor | null | undefined;
  return {
    items: Array.isArray(envelope?.items) ? envelope.items : [],
    next: next?.before_created_at && next?.before_id ? next : null,
    total: typeof envelope?.total === "number" ? envelope.total : 0,
  };
}

export interface UploadResponse {
  url: string;
}

// ---- Auth -------------------------------------------------------------

export function getMe(): Promise<User> {
  return apiFetch<User>("/auth/me");
}

// What both login paths return once the hora_session cookie is set —
// /auth/exchange and /auth/review-login share one response shape because they
// share one session issuer (server/main.go issueHoraSession).
export interface SessionIdentity {
  id?: string;
  email?: string;
  name?: string;
}

// App Review bypass: the backend accepts a fixed code for exactly one
// env-configured review account and answers 401 for everything else, so the
// client never has to know which address that is — it just retries a code
// Supabase rejected. Off entirely unless the server has REVIEW_ACCOUNT_EMAIL
// and REVIEW_ACCOUNT_CODE set, in which case the route 404s.
export function reviewLogin(email: string, code: string): Promise<SessionIdentity> {
  return apiFetch<SessionIdentity>("/auth/review-login", {
    method: "POST",
    body: { email, code },
  });
}

export function logout(): Promise<void> {
  return apiFetch<void>("/auth/logout", { method: "POST" });
}

// ---- Profile ------------------------------------------------------------

export function getProfile(): Promise<Profile> {
  return apiFetch<Profile>("/profile");
}

export interface UpdateProfilePatch {
  name?: string;
  phone?: string;
  city?: string;
  avatar_url?: string;
  bio?: string;
  beta_accepted?: boolean;
}

export function updateProfile(patch: UpdateProfilePatch): Promise<Profile> {
  return apiFetch<Profile>("/profile", { method: "PATCH", body: patch });
}

export async function uploadAvatar(file: FilePart): Promise<UploadResponse> {
  return apiFetch<UploadResponse>("/profile/avatar", {
    method: "POST",
    body: await buildFileFormData(file, "file"),
  });
}

export function getPublicProfile(id: string): Promise<PublicProfile> {
  return apiFetch<PublicProfile>(`/profiles/${id}`);
}

export interface ProfileTasksParams {
  role?: "requester" | "assignee";
  status?: "open" | "completed" | "all";
  limit?: number;
  before?: string;
}

export function getProfileTasks(id: string, params?: ProfileTasksParams): Promise<Task[]> {
  return apiFetch<Task[]>(`/profiles/${id}/tasks${toQueryString(params)}`);
}

// server/main.go's listProfileReviews wraps rows under a `reviews` key
// ({ reviews, count, avg_stars }), not a bare array — unwrapped here so a
// null/missing list can't reach a screen's .map().
interface ReviewsEnvelope {
  reviews: Review[] | null;
  count: number;
  avg_stars: number | null;
}

export function getProfileReviews(id: string): Promise<Review[]> {
  return apiFetch<ReviewsEnvelope>(`/profiles/${id}/reviews`).then((envelope) =>
    Array.isArray(envelope?.reviews) ? envelope.reviews : []
  );
}

export interface SupporterReviewsSummary {
  reviews: Review[];
  count: number;
  avgStars: number | null;
}

// Same endpoint as getProfileReviews, but keeps count/avg_stars instead of
// discarding them — for screens that need to show an aggregate, not just
// the list.
export function getSupporterReviews(id: string): Promise<SupporterReviewsSummary> {
  return apiFetch<ReviewsEnvelope>(`/profiles/${id}/reviews`).then((envelope) => ({
    reviews: Array.isArray(envelope?.reviews) ? envelope.reviews : [],
    count: envelope?.count ?? 0,
    avgStars: envelope?.avg_stars ?? null,
  }));
}

// ---- Supporter ------------------------------------------------------------

export interface ApplySupporterPayload {
  first_name?: string;
  last_name?: string;
}

export function applySupporter(payload: ApplySupporterPayload): Promise<{ ok: true }> {
  return apiFetch<{ ok: true }>("/supporter/apply", { method: "POST", body: payload });
}

// ---- Tasks (requester) -----------------------------------------------------

export interface CreateTaskPayload {
  title: string;
  description?: string;
  category: TaskCategory;
  location_text?: string;
  estimated_minutes?: number;
  prepay_amount_cents?: number;
  is_immediate: boolean;
  scheduled_at?: string;
  transport_required?: string;
  /** Attribution for the October repeat-usage count. Omitted on PATCH — see
   *  UpdateTaskPayload, which drops it: an edit doesn't change how a task was
   *  created, and the server would ignore it there anyway. */
  created_via?: TaskCreatedVia;
  /**
   * Consent for the supporter to run up to 15 minutes past the estimate
   * without stopping to ask (tasks.auto_extend_consent).
   *
   * Optional, and the server reads an absent field as "unchanged" rather than
   * "refused" — the column defaults to true, which is the behaviour every task
   * posted before this field existed already had.
   */
  auto_extend_consent?: boolean;
}

export function createTask(payload: CreateTaskPayload): Promise<Task> {
  return apiFetch<Task>("/tasks", { method: "POST", body: payload });
}

export interface EstimateTaskCostPayload {
  category: TaskCategory;
  estimated_minutes: number;
  prepay_amount_cents: number;
  /** When the work would start. Decides the rate — the evening band is a
   *  property of when the task happens, not of when the form was opened. */
  is_immediate?: boolean;
  scheduled_at?: string;
}

export interface TaskCostEstimate {
  base_fee_cents: number;
  time_cost_cents: number;
  total_cents: number;

  /** Minutes covered by the base fee (15). Added in the Stripe Phase 1 backend. */
  included_minutes?: number;
  /** max(estimated_minutes - included_minutes, 0) — what time_cost_cents prices. */
  billable_minutes?: number;
  /** The per-minute rate, so no client hardcodes "$0.50". */
  per_minute_rate_cents?: number;
  /** Replaces `shopping_cents`. Both are sent; prefer this one. */
  shopping_budget_cents?: number;
  /** Whether the evening rate applies to this quote. */
  surge_rate?: boolean;
  /** What posting will actually reserve. Identical to total_cents — the hold
   *  IS the estimate plus the budget — and sent separately because that
   *  identity is a design decision, not a coincidence a client should assume. */
  hold_cents?: number;
  /** Where the post form starts warning about a large reservation. Server-owned
   *  so both clients warn at the same number (S-05). */
  high_budget_warning_cents?: number;

  /**
   * @deprecated The pre-Phase-1 name for `shopping_budget_cents`. The backend
   * still sends it so that builds shipped before this change keep rendering,
   * and it is optional here so a future backend can drop it without a type
   * error. Read `shopping_budget_cents ?? shopping_cents`.
   */
  shopping_cents?: number;
}

// Pricing is server-owned (S-01/S-05) — this is the only source for the
// pre-submission estimate shown on the Post Task review screen.
export function estimateTaskCost(payload: EstimateTaskCostPayload): Promise<TaskCostEstimate> {
  return apiFetch<TaskCostEstimate>("/tasks/estimate", { method: "POST", body: payload });
}

// ---- Payments (card on file) ----------------------------------------------
//
// The Go backend is the only thing that talks to Stripe's REST API; this app
// talks to Stripe only through the native SDK, with secrets minted here. No
// card number ever passes through these types — PaymentSheet collects it
// inside Stripe's own native UI and the app never sees it, which is what keeps
// the app out of PCI scope.

/** Everything PaymentSheet needs to present in setup mode, in one call. */
export interface SetupIntentSession {
  client_secret: string;
  customer_id: string;
  ephemeral_key: string;
  /** Sent by the backend so a key rotation needs no native rebuild. */
  publishable_key: string;
  merchant_display_name: string;
}

export function createSetupIntent(): Promise<SetupIntentSession> {
  return apiFetch<SetupIntentSession>("/payments/setup-intent", { method: "POST" });
}

export interface SavedCard {
  id: string;
  brand: string;
  last4: string;
  exp_month: number;
  exp_year: number;
  /** The card a new task's hold would be placed on. */
  is_default: boolean;
}

export interface PaymentMethodsResponse {
  cards: SavedCard[];
  has_card: boolean;
  publishable_key: string;
  /**
   * Whether posting currently requires a card (the backend's PAYMENTS_ENFORCED
   * flag). Advisory: POST /tasks answers 402 regardless of what a client
   * believes. It is here so the card prompt appears before a requester fills
   * in a form, and so nothing about payments is shown at all while it is off.
   */
  payments_enforced: boolean;
}

export function getPaymentMethods(): Promise<PaymentMethodsResponse> {
  return apiFetch<PaymentMethodsResponse>("/payments/payment-methods");
}

export function deletePaymentMethod(id: string): Promise<{ ok: true }> {
  return apiFetch<{ ok: true }>(`/payments/payment-methods/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

/**
 * Post a task whose hold needed the cardholder present. Called after the SDK's
 * handleNextAction resolves; the backend reads the intent's real status from
 * Stripe rather than believing this call, so it cannot be used to post an
 * unfunded task.
 */
export function confirmTaskPayment(
  taskId: string
): Promise<{ ok: true; status: string; payment?: TaskPayment }> {
  return apiFetch<{ ok: true; status: string; payment?: TaskPayment }>(
    `/tasks/${encodeURIComponent(taskId)}/payment/confirm`,
    { method: "POST" }
  );
}

// ---- Outstanding balance ---------------------------------------------------
//
// A completion that settled above its hold and could not be charged leaves a
// balance. While one exists, POST /tasks answers 403 outstanding_balance and
// both clients show a persistent banner. Supporters are never gated on it —
// payouts go out regardless and the platform carries the float.

export function getOutstandingBalance(): Promise<{ outstanding: OutstandingBalance | null }> {
  return apiFetch<{ outstanding: OutstandingBalance | null }>("/payments/outstanding-balance");
}

export interface SettleBalanceResult {
  ok?: true;
  settled_cents: number;
  outstanding: OutstandingBalance | null;
}

/**
 * Retry every outstanding charge off-session.
 *
 * Throws ApiError(402) with `client_secret` when the issuer wants the
 * cardholder present — run the challenge with the Stripe SDK and call this
 * again. That is the one failure a retry can actually fix, and the reason this
 * is a button rather than a background job.
 */
export function settleBalance(): Promise<SettleBalanceResult> {
  return apiFetch<SettleBalanceResult>("/payments/settle-balance", { method: "POST" });
}

// ---- Tasks (requester), continued ------------------------------------------

export function getPostedTasks(params?: KeysetParams): Promise<Task[]> {
  return apiFetch<KeysetEnvelope<Task>>(`/tasks/posted${toQueryString(params)}`).then(unwrapItems);
}

export function getPostedClosedTasks(params?: KeysetParams): Promise<Task[]> {
  return apiFetch<KeysetEnvelope<Task>>(`/tasks/posted/closed${toQueryString(params)}`).then(unwrapItems);
}

/** The requester's closed tasks as a PAGE — items, cursor and total. */
export function getPostedClosedPage(params?: KeysetParams): Promise<TaskPage> {
  return apiFetch<KeysetEnvelope<Task>>(`/tasks/posted/closed${toQueryString(params)}`).then(unwrapPage);
}

export function getTask(id: string): Promise<Task> {
  return apiFetch<Task>(`/tasks/${id}`);
}

/**
 * Despite the verb, PATCH /tasks/:id is a full replace: the handler binds the
 * same createTaskInput as POST /tasks and writes every column it names, so a
 * field left out is not preserved — it is blanked, or replaced by that field's
 * create-time default (estimated_minutes → 30, category → quick_errand,
 * prepay → 0). Always send the complete form; `taskFormToPayload` builds it.
 * The single exception is transport_required, which the server keeps when the
 * value is empty so web's edit payload (which omits it) can't wipe it.
 *
 * The server also enforces that only the requester's own open, unassigned task
 * can be edited, and rejects a save that races an accept with
 * "cannot edit after it has been accepted".
 */
// Same body as POST /tasks minus the create-only attribution field: PATCH
// /tasks/:id binds the same Go struct but never writes created_via, so sending
// it would be noise on the wire and a lie in the type.
export type UpdateTaskPayload = Omit<CreateTaskPayload, "created_via">;

export function updateTask(id: string, payload: UpdateTaskPayload): Promise<Task> {
  return apiFetch<Task>(`/tasks/${id}`, { method: "PATCH", body: payload });
}

// server/main.go's cancelTask does NOT return the updated Task — it returns
// the billing summary for the cancellation.
//
// The old comment here said the task was "necessarily unworked, since
// cancellation requires assigned_to_id IS NULL". That has not been true since
// the cancellation billing policy landed: a requester can cancel an accepted
// or in-progress task, an open session is force-closed and billed, and the
// base fee is paid to the supporter as their guarantee.
export interface CancelTaskResult {
  total_minutes: number;
  bill_cents: number;
  refund_cents: number;
  /** Which rule ran. `committed` is false only on a task nobody accepted;
   *  `within_grace` is the free window that makes a committed cancel cost
   *  nothing. Neither can be inferred from bill_cents — a charged cancel of a
   *  $0-base-fee task would be indistinguishable from a free one. */
  committed?: boolean;
  within_grace?: boolean;
  /** How the bill is made up, so the confirmation never re-derives it (S-05). */
  base_fee_cents?: number;
  time_cost_cents?: number;
  billed_minutes?: number;
  /** What the hold was, what was taken from it, and what went back. All three
   *  are 0 on a task that never had one — the confirmation then says nothing
   *  about money rather than "$0.00 released". */
  authorized_cents?: number;
  captured_cents?: number;
  released_cents?: number;
  /** The card the money is going back to, when it is known. */
  card_brand?: string;
  card_last4?: string;
}

/**
 * `reason` is the free text, which stays on the row for ops and the audit log.
 * `reasonCode` is the preset slug, and it is the ONLY part of the reason ever
 * relayed to the supporter — see server/cancel_reasons.go for why the two are
 * separate.
 */
export function cancelTask(
  id: string,
  reason: string,
  reasonCode?: string
): Promise<CancelTaskResult> {
  return apiFetch<CancelTaskResult>(`/tasks/${id}/cancel`, {
    method: "POST",
    body: { reason, reason_code: reasonCode },
  });
}

export interface CompleteTaskPayload {
  completion_photo_url: string;
  completion_note?: string;
  /**
   * What the receipt came to, in integer cents (Stripe Phase 2b).
   *
   * REQUIRED on a task with an approved shopping budget, and 0 is a perfectly
   * good answer there — it means nothing was bought. Omitting it entirely on
   * such a task is refused with `receipt_required`, deliberately: silence from
   * a client that knows nothing about receipts must not be read as "$0" on a
   * task somebody shopped for.
   *
   * The server refuses anything above the approved budget plus the $5
   * auto-approved tolerance with `receipt_exceeds_budget` — the supporter's
   * way through that is a budget increase, not a bigger number here.
   */
  receipt_amount_cents?: number;
  /** Required whenever `receipt_amount_cents` is above zero. Upload it through
   * `uploadCompletionPhoto`, the same storage path the completion photo uses. */
  receipt_photo_url?: string;
}

export function completeTask(id: string, payload: CompleteTaskPayload): Promise<Task> {
  return apiFetch<Task>(`/tasks/${id}/complete`, { method: "POST", body: payload });
}

// server/main.go's createReview requires stars (1-5) from a REQUESTER;
// value_rating is validated only when non-empty and would_rehire is a nullable
// pointer — both genuinely optional, matching web's ReviewPage.jsx which lets
// either go unset. stars is optional here because the supporter questionnaire
// rates nobody and sends none; the server drops any it is sent on that path.
export interface SubmitReviewPayload {
  stars?: number;
  value_rating?: ValueRating;
  would_rehire?: boolean;
  comment?: string;
  // Traction 3 questionnaire. The server derives rater_role and the ratee from
  // the caller's relation to the task, so neither is (or can be) sent here.
  ease_rating?: EaseRating;
  would_use_again?: WouldUseAgain;
  open_feedback?: string;
}

export function submitReview(id: string, payload: SubmitReviewPayload): Promise<Review> {
  return apiFetch<Review>(`/tasks/${id}/review`, { method: "POST", body: payload });
}

// ---- Tasks (supporter) -----------------------------------------------------

export function getAvailableTasks(params?: KeysetParams): Promise<Task[]> {
  return apiFetch<KeysetEnvelope<Task>>(`/tasks/available${toQueryString(params)}`).then(unwrapItems);
}

export function getAssignedTasks(params?: KeysetParams): Promise<Task[]> {
  return apiFetch<KeysetEnvelope<Task>>(`/tasks/assigned${toQueryString(params)}`).then(unwrapItems);
}

export function getDoneTasks(params?: KeysetParams): Promise<Task[]> {
  return apiFetch<KeysetEnvelope<Task>>(`/tasks/done${toQueryString(params)}`).then(unwrapItems);
}

/** The supporter's finished tasks as a PAGE — items, cursor and total. */
export function getDonePage(params?: KeysetParams): Promise<TaskPage> {
  return apiFetch<KeysetEnvelope<Task>>(`/tasks/done${toQueryString(params)}`).then(unwrapPage);
}

export function acceptTask(id: string): Promise<Task> {
  return apiFetch<Task>(`/tasks/${id}/accept`, { method: "POST" });
}

// server/main.go's WorkLog struct serializes as `start`/`end` (not
// `start_at`/`end_at`), and `End *time.Time` carries `json:"end,omitempty"` —
// when a worklog is still open (End is nil), the `end` key is OMITTED from
// the JSON entirely rather than sent as `null`. clock-in/out and getWorklogs
// all return this same shape; mapWorklogDTO is the one place that normalizes
// it into mobile's Worklog type, so "open worklog" can reliably be checked
// as `end_at === null` everywhere else instead of `undefined`.
interface WorklogDTO {
  id: string;
  task_id: string;
  user: string;
  start: string;
  end?: string | null;
  created_at: string;
  updated_at: string;
}

function mapWorklogDTO(wl: WorklogDTO): Worklog {
  return {
    id: wl.id,
    task_id: wl.task_id,
    user: wl.user,
    start_at: wl.start,
    end_at: wl.end ?? null,
    created_at: wl.created_at,
    updated_at: wl.updated_at,
  };
}

export function clockIn(id: string): Promise<Worklog> {
  return apiFetch<WorklogDTO>(`/tasks/${id}/clock-in`, { method: "POST" }).then(mapWorklogDTO);
}

export function clockOut(id: string): Promise<Worklog> {
  return apiFetch<WorklogDTO>(`/tasks/${id}/clock-out`, { method: "POST" }).then(mapWorklogDTO);
}

export interface GpsPingPayload {
  lat: number;
  lng: number;
  accuracy?: number;
  // Which capture path produced this fix. Optional on the wire — the backend
  // defaults it to "foreground" so the TestFlight build that predates
  // background tracking keeps working unchanged.
  // "enroute" is the pre-clock-in window (POST /tasks/:id/enroute). It is the
  // one source the server accepts with NO open worklog, and only inside that
  // window — see enrouteWindowOpen in server/live_tracking.go.
  source?: "foreground" | "background" | "enroute";
}

export function sendGpsPing(id: string, payload: GpsPingPayload): Promise<GpsPing> {
  return apiFetch<GpsPing>(`/tasks/${id}/gps-ping`, { method: "POST", body: payload });
}

// The requester (or assignee) reads the supporter's most recent ping. The
// backend replies `200 null` when no ping exists yet — apiFetch resolves that
// empty body to null, which the caller renders as the "waiting" state rather
// than an error. Mirrors web's /tasks/:id/gps-latest poll (app/src/pages/
// TaskDetail.jsx).
export function getLatestLocation(id: string): Promise<LatestLocation | null> {
  return apiFetch<LatestLocation | null>(`/tasks/${id}/gps-latest`);
}

/**
 * The supporter says they have set off. Opens the pre-clock-in sharing window
 * and nothing else — until this is called, the requester sees nothing.
 *
 * Idempotent on the server: a second call returns the first call's timestamp
 * rather than restarting the window.
 */
export function startEnroute(id: string): Promise<{ enroute_at: string }> {
  return apiFetch<{ enroute_at: string }>(`/tasks/${id}/enroute`, { method: "POST" });
}

/**
 * The requester's live read. Polled every 15s while the screen is focused and
 * the app is in front (LiveTrackingCard owns that rule).
 *
 * 404s for anyone but the requester and for any task that is not open with
 * somebody on it — so callers gate on shouldPollLive rather than catching.
 */
export function getLiveLocation(id: string): Promise<LiveLocation> {
  return apiFetch<LiveLocation>(`/tasks/${id}/live`);
}

export interface EstimateTravelPayload {
  supporter_lat: number;
  supporter_lng: number;
}

export function estimateTravel(id: string, payload: EstimateTravelPayload): Promise<TravelEstimate> {
  return apiFetch<TravelEstimate>(`/tasks/${id}/estimate-travel`, { method: "POST", body: payload });
}

// ---- Shared (requester + supporter) ----------------------------------------

// The backend's actual envelope nests rows under `items` (server/main.go
// getWorklogs), not a bare `worklogs` array — unwrapped here, via the same
// mapWorklogDTO used by clockIn/clockOut, so a null/missing list can't reach
// a screen and every worklog's field names/nullability match the wire.
interface WorklogsEnvelope {
  items: WorklogDTO[] | null;
  total_minutes: number;
  total_cost_cents: number;
  cost?: TaskCost;
  settlement?: Settlement;
}

// The one settlement surface, for both roles and at every stage of a task —
// "what does this cost so far" and "what was I charged" are the same question
// asked at two moments, so they are the same call (Stripe Phase 2b extended
// this payload rather than adding a parallel endpoint). `cost` and
// `settlement` are optional here so a build newer than the backend degrades to
// the totals rather than crashing on a missing key.
export function getWorklogs(id: string): Promise<WorklogsSummary> {
  return apiFetch<WorklogsEnvelope>(`/tasks/${id}/worklogs`).then((envelope) => ({
    worklogs: (Array.isArray(envelope?.items) ? envelope.items : []).map(mapWorklogDTO),
    total_minutes: envelope?.total_minutes ?? 0,
    total_cost_cents: envelope?.total_cost_cents ?? 0,
    cost: envelope?.cost ?? null,
    settlement: envelope?.settlement ?? null,
  }));
}

// ---- Mid-task asks (Stripe Phase 2b) ---------------------------------------
//
// A supporter needs more money or more time; the requester answers with one
// tap. Silence for `timeout_minutes` is a denial, and the supporter's
// pre-selected fallback is what happens then — which is why the fallback is
// mandatory on a budget request and asked for BEFORE the wait.
//
// Expiry is applied by the server on every read of this list, so a screen that
// polls it sees the timeout land within one poll of the deadline. There is no
// separate "check if it expired" call, and none is needed.

export function getExtensions(taskId: string): Promise<ExtensionsResponse> {
  return apiFetch<ExtensionsResponse>(`/tasks/${taskId}/extensions`).then((res) => ({
    ...res,
    items: Array.isArray(res?.items) ? res.items : [],
    time_choices: Array.isArray(res?.time_choices) ? res.time_choices : [15, 30],
  }));
}

export interface BudgetIncreasePayload {
  /** The ADDITIONAL cents needed, not a new total. */
  requested_cents: number;
  reason?: string;
  /** What to do if the answer is no, or never comes. Required. */
  fallback: ExtensionFallback;
  /** Describes the alternative, when `fallback` is "buy_alternative". Without
   * it the supporter is later told to "buy the alternative you chose" with no
   * record of what that was. */
  fallback_note?: string;
}

export function requestBudgetIncrease(
  taskId: string,
  payload: BudgetIncreasePayload
): Promise<ExtensionRequest> {
  return apiFetch<ExtensionRequest>(`/tasks/${taskId}/budget-increase`, {
    method: "POST",
    body: payload,
  });
}

/** `minutes` must be one of the server's `time_choices` (15 or 30 today). */
export function requestTimeExtension(taskId: string, minutes: number): Promise<ExtensionRequest> {
  return apiFetch<ExtensionRequest>(`/tasks/${taskId}/time-extension`, {
    method: "POST",
    body: { requested_minutes: minutes },
  });
}

export interface ExtensionResolution {
  request: ExtensionRequest;
  approved_budget_cents: number;
  time_cap: ExtensionsResponse["time_cap"];
}

// Requester only. A request that has already been answered — or that timed out
// a second before the tap landed — answers 409 with the current status rather
// than silently charging for something the supporter has already worked around.
export function resolveExtension(
  taskId: string,
  extensionId: string,
  decision: "approve" | "deny"
): Promise<ExtensionResolution> {
  return apiFetch<ExtensionResolution>(
    `/tasks/${taskId}/extensions/${extensionId}/${decision}`,
    { method: "POST" }
  );
}

export async function uploadCompletionPhoto(id: string, file: FilePart): Promise<UploadResponse> {
  return apiFetch<UploadResponse>(`/tasks/${id}/completion-photo`, {
    method: "POST",
    body: await buildFileFormData(file, "file"),
  });
}

// ---- AI ---------------------------------------------------------------

export function parseTask(input: string): Promise<ParsedTask> {
  return apiFetch<ParsedTask>("/ai/parse-task", { method: "POST", body: { input } });
}

// ---- Notifications ----------------------------------------------------

export interface NotificationsParams {
  unread?: boolean;
  limit?: number;
  before?: string;
}

// GET /notifications replies 204 (empty body) when tryAuth finds no session,
// which apiFetch parses as `null` rather than throwing — guarded here so a
// missing/null list can't reach a screen's .map().
export function getNotifications(params?: NotificationsParams): Promise<AppNotification[]> {
  return apiFetch<AppNotification[] | null>(`/notifications${toQueryString(params)}`).then((list) =>
    Array.isArray(list) ? list : []
  );
}

// server/main.go's markNotificationRead returns 204 (empty body), not the
// updated row — apiFetch resolves that to `null`, so callers must update
// their local notification state optimistically rather than from a response.
export function markNotificationRead(id: string): Promise<void> {
  return apiFetch<void>(`/notifications/${id}/read`, { method: "PATCH" });
}

export function markAllRead(): Promise<void> {
  return apiFetch<void>("/notifications/mark-read-all", { method: "POST" });
}

export function deleteNotification(id: string): Promise<void> {
  return apiFetch<void>(`/notifications/${id}`, { method: "DELETE" });
}

export function deleteReadNotifications(): Promise<void> {
  return apiFetch<void>("/notifications?read=true", { method: "DELETE" });
}

// ---- Push tokens ------------------------------------------------------

// Register this device's Expo push token so the Go backend can deliver
// task-event push (accept / clock-in / clock-out / complete / cancel). The
// server upserts on the globally-unique token, so this is idempotent and safe
// to call on every login / app-start.
export function registerPushToken(token: string, platform: string): Promise<{ ok: true }> {
  return apiFetch<{ ok: true }>("/push/register", { method: "POST", body: { token, platform } });
}

// Drop this device's token on logout so a signed-out phone stops receiving the
// previous user's task push.
export function unregisterPushToken(token: string): Promise<{ ok: true }> {
  return apiFetch<{ ok: true }>("/push/unregister", { method: "POST", body: { token } });
}

// ---- TalkJS -------------------------------------------------------------

// Hex HMAC-SHA256 of the caller's TalkJS user id (their email — matching the
// existing Talk.User id scheme), computed server-side with TALKJS_SECRET_KEY.
// Pass this on Session so TalkJS can verify the client isn't impersonating
// another user's identity.
export function getTalkjsSignature(): Promise<string> {
  return apiFetch<{ signature: string }>("/talkjs/signature").then((res) => res.signature);
}

export { ApiError } from "./api-error";

// ── Phase 3: getting paid ──────────────────────────────────────────────────
//
// The supporter's half of the money. Nothing here ever touches a bank account,
// an SSN or an identity document — Stripe collects all of it, on its own
// domain, through a URL this backend mints. The app only ever holds that URL
// and a status word.

/** not_started → in_progress → verifying → complete. The Earnings screen is
 *  a machine over this. "verifying" is the gap between the form being in and
 *  Stripe making the account transferable — nothing to do but wait. */
export type OnboardingState = "not_started" | "in_progress" | "verifying" | "complete";

export interface ConnectStatus {
  state: OnboardingState;
  /** The account can reach a bank. */
  payouts_enabled: boolean;
  /** The platform can reach the account. Both must be true to accept tasks. */
  transfers_active: boolean;
  details_submitted: boolean;
  /** Stripe's own field names. Render the COUNT, never the names. */
  requirements_due: string[];
  /** Whether accepting is currently gated on payouts (PAYMENTS_ENFORCED). */
  payouts_enforced: boolean;
}

/** The one word a payout row renders. "paid" is reserved for money in the BANK. */
export type TransferDisplayStatus = "paid" | "on_its_way" | "failed";

export interface EarningsTransfer {
  task_id: string;
  task_title: string;
  amount_cents: number;
  time_cents: number;
  receipt_cents: number;
  /** Row status: paid means the TRANSFER happened, not that the bank has it. */
  status: "pending" | "paid" | "failed";
  created_at: string;
  /** When the bank payout carrying this transfer landed; null while on its way. */
  bank_paid_at?: string | null;
  /** Decided by the backend so both clients agree (S-05). */
  display_status?: TransferDisplayStatus;
}

export interface Earnings {
  onboarding: ConnectStatus;
  /** EARNED: the sum of successful transfers, wherever the money sits now.
   *  Failed transfers never count. */
  lifetime_earned_cents: number;
  /** The split of lifetime_earned_cents by where it is. They add up. */
  paid_out_cents?: number;
  in_transit_cents?: number;
  transfers: EarningsTransfer[];
  /** How many transfers exist in total, so a preview showing three can say
   *  "See all (47)". Distinct from transfers.length, which is one page. */
  total?: number;
}

/**
 * Start or resume payout onboarding.
 *
 * The returned URL is SINGLE-USE and grants access to the supporter's own
 * personal information. Open it immediately in an in-app browser; never store
 * it, never log it, never send it anywhere.
 */
export function createOnboardingLink(): Promise<{ url: string }> {
  return apiFetch<{ url: string }>("/payments/connect/onboarding-link", { method: "POST" });
}

export function getConnectStatus(): Promise<ConnectStatus> {
  return apiFetch<ConnectStatus>("/payments/connect/status");
}

/** One-time URL into the Stripe Express dashboard. 404 before onboarding. */
export function createLoginLink(): Promise<{ url: string }> {
  return apiFetch<{ url: string }>("/payments/connect/login-link", { method: "POST" });
}

export function getEarnings(params?: { limit?: number; offset?: number }): Promise<Earnings> {
  return apiFetch<Earnings>(`/payments/earnings${toQueryString(params)}`);
}

/**
 * Whether a failed accept was the payouts gate rather than a real failure.
 *
 * 403 and not 402: nothing is owed and no payment is required — the supporter
 * simply has nowhere for money to land yet, and the only useful response is the
 * onboarding CTA rather than a retry.
 */
export function isPayoutsOnboardingRequired(e: unknown): boolean {
  if (!(e instanceof ApiError) || e.status !== 403) return false;
  const body = (e.body ?? {}) as Record<string, unknown>;
  return body.error === "payouts_onboarding_required";
}
