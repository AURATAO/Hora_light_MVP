import { ApiError } from "./api-error";
import type {
  AppNotification,
  GpsPing,
  ParsedTask,
  Profile,
  PublicProfile,
  Review,
  Task,
  TaskCategory,
  TravelEstimate,
  User,
  ValueRating,
  Worklog,
  WorklogsSummary,
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

// React Native's FormData accepts { uri, name, type } file parts at runtime;
// the DOM lib.d.ts types FormData.append as (name, value: string | Blob), so
// this cast is required to describe RN's actual behavior (not the DOM's).
function buildFileFormData(fileUri: string, fieldName: string): FormData {
  const filename = fileUri.split("/").pop() ?? `upload-${Date.now()}`;
  const ext = /\.(\w+)$/.exec(filename)?.[1]?.toLowerCase();
  const type = ext === "png" ? "image/png" : ext === "heic" ? "image/heic" : "image/jpeg";

  const formData = new FormData();
  formData.append(fieldName, { uri: fileUri, name: filename, type } as unknown as Blob);
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

export function uploadAvatar(fileUri: string): Promise<UploadResponse> {
  return apiFetch<UploadResponse>("/profile/avatar", {
    method: "POST",
    body: buildFileFormData(fileUri, "file"),
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

export type UpdateTaskPatch = Partial<
  Pick<
    Task,
    | "title"
    | "description"
    | "category"
    | "location_text"
    | "estimated_minutes"
    | "prepay_amount_cents"
    | "is_immediate"
    | "scheduled_at"
    | "transport_required"
  >
>;

export function updateTask(id: string, patch: UpdateTaskPatch): Promise<Task> {
  return apiFetch<Task>(`/tasks/${id}`, { method: "PATCH", body: patch });
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

// server/main.go's createReview only requires stars (1-5); value_rating is
// validated only when non-empty and would_rehire is a nullable pointer — both
// genuinely optional, matching web's ReviewPage.jsx which lets either go unset.
export interface SubmitReviewPayload {
  stars: number;
  value_rating?: ValueRating;
  would_rehire?: boolean;
  comment?: string;
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
}

export function sendGpsPing(id: string, payload: GpsPingPayload): Promise<GpsPing> {
  return apiFetch<GpsPing>(`/tasks/${id}/gps-ping`, { method: "POST", body: payload });
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

export function uploadCompletionPhoto(id: string, fileUri: string): Promise<UploadResponse> {
  return apiFetch<UploadResponse>(`/tasks/${id}/completion-photo`, {
    method: "POST",
    body: buildFileFormData(fileUri, "file"),
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

export function markNotificationRead(id: string): Promise<AppNotification> {
  return apiFetch<AppNotification>(`/notifications/${id}/read`, { method: "PATCH" });
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

export { ApiError } from "./api-error";
