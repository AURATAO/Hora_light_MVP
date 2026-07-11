export const API_BASE_URL = "https://core.horaapp.co";

type ApiFetchOptions = Omit<RequestInit, "body"> & { body?: unknown };

export class ApiError extends Error {
  status: number;
  body: unknown;

  constructor(message: string, status: number, body: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

// All Hora business data (tasks, profiles, notifications, ...) goes through
// the Go backend, never a direct Supabase table read (S-01). This is the one
// fetch wrapper client code should use for that traffic.
export async function apiFetch(path: string, options: ApiFetchOptions = {}) {
  const { body, headers, ...rest } = options;

  const response = await fetch(`${API_BASE_URL}${path}`, {
    ...rest,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...headers,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });

  const text = await response.text();
  const data = text ? JSON.parse(text) : null;

  if (!response.ok) {
    throw new ApiError(
      (data && typeof data === "object" && "error" in data && String(data.error)) ||
        `Request failed: ${response.status}`,
      response.status,
      data
    );
  }

  return data;
}
