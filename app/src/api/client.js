const API_BASE = import.meta.env.VITE_API_BASE_URL || "http://localhost:8080";

function getToken() {
  return localStorage.getItem("hora_token") || "";
}

export async function api(path, { method = "GET", body, headers = {} } = {}) {
  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers: {
      "Content-Type": "application/json",
      ...(getToken() ? { Authorization: `Bearer ${getToken()}` } : {}),
      ...headers,
    },
    body: body ? JSON.stringify(body) : undefined,
    // credentials: "include", // harmless if server doesn't set cookies
  });

  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(text || `HTTP ${res.status}`);
  }
  // try parse JSON; fallback text
  const ct = res.headers.get("content-type") || "";
  if (ct.includes("application/json")) return res.json();
  return res.text();
}

export const AuthAPI = {
  requestOtp: (email) =>
    api("/auth/request-otp", { method: "POST", body: { email } }),
  // Optional magic link path if you enable it on backend:
  // requestMagicLink: (email) => api('/auth/request-magic-link', { method: 'POST', body: { email } }),
  verifyOtp: (email, code) =>
    api("/auth/verify", { method: "POST", body: { email, code } }),
  me: () => api("/auth/me"),
};
