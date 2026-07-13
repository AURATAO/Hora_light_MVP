interface BackendErrorBody {
  error?: string;
}

function isBackendErrorBody(body: unknown): body is BackendErrorBody {
  return typeof body === "object" && body !== null && "error" in body;
}

export class ApiError extends Error {
  readonly status: number;
  readonly body: unknown;
  /** True for 401 responses, so screens can redirect to login without a status check. */
  readonly isAuthError: boolean;

  constructor(status: number, body: unknown) {
    const message =
      isBackendErrorBody(body) && typeof body.error === "string"
        ? body.error
        : `Request failed: ${status}`;
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
    this.isAuthError = status === 401;
  }
}
