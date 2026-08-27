import { ApiError } from "./api-error";
import type {
  AppNotification,
  EaseRating,
  GpsPing,
  LatestLocation,
  ParsedTask,
  Profile,
  PublicProfile,
  Review,
  Task,
  TaskCategory,
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
}

// Every keyset-paginated task list endpoint (/tasks/posted, /posted/closed,
// /available, /assigned, /done) wraps its rows in { items, next }, not a bare
// array — and a handler that ever built `items` from a nil slice would
// serialize it as JSON `null`. Route every caller through this so a
// missing/null list can never reach a screen's .map().
interface KeysetEnvelope<T> {
  items: T[] | null;
  next?: unknown;
}

function unwrapItems<T>(envelope: KeysetEnvelope<T> | null | undefined): T[] {
  return Array.isArray(envelope?.items) ? envelope.items : [];
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
}

export function createTask(payload: CreateTaskPayload): Promise<Task> {
  return apiFetch<Task>("/tasks", { method: "POST", body: payload });
}

export interface EstimateTaskCostPayload {
  category: TaskCategory;
  estimated_minutes: number;
  prepay_amount_cents: number;
}

export interface TaskCostEstimate {
  base_fee_cents: number;
  time_cost_cents: number;
  shopping_cents: number;
  total_cents: number;
}

// Pricing is server-owned (S-01/S-05) — this is the only source for the
// pre-submission estimate shown on the Post Task review screen.
export function estimateTaskCost(payload: EstimateTaskCostPayload): Promise<TaskCostEstimate> {
  return apiFetch<TaskCostEstimate>("/tasks/estimate", { method: "POST", body: payload });
}

export function getPostedTasks(params?: KeysetParams): Promise<Task[]> {
  return apiFetch<KeysetEnvelope<Task>>(`/tasks/posted${toQueryString(params)}`).then(unwrapItems);
}

export function getPostedClosedTasks(params?: KeysetParams): Promise<Task[]> {
  return apiFetch<KeysetEnvelope<Task>>(`/tasks/posted/closed${toQueryString(params)}`).then(unwrapItems);
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
// the billing summary for the (necessarily unworked, since cancellation
// requires assigned_to_id IS NULL) cancellation.
export interface CancelTaskResult {
  total_minutes: number;
  bill_cents: number;
  refund_cents: number;
}

export function cancelTask(id: string, reason: string): Promise<CancelTaskResult> {
  return apiFetch<CancelTaskResult>(`/tasks/${id}/cancel`, { method: "POST", body: { reason } });
}

export interface CompleteTaskPayload {
  completion_photo_url: string;
  completion_note?: string;
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
  source?: "foreground" | "background";
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
}

export function getWorklogs(id: string): Promise<WorklogsSummary> {
  return apiFetch<WorklogsEnvelope>(`/tasks/${id}/worklogs`).then((envelope) => ({
    worklogs: (Array.isArray(envelope?.items) ? envelope.items : []).map(mapWorklogDTO),
    total_minutes: envelope?.total_minutes ?? 0,
    total_cost_cents: envelope?.total_cost_cents ?? 0,
  }));
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
